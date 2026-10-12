package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/backup"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func TestAuthenticationOriginCSRFAndPrivacy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, e := store.Open(ctx, filepath.Join(root, "test.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	_, e = db.DB.ExecContext(ctx, "INSERT INTO users(id,name,code,role,status,created_at) VALUES(1,'SECRET_PRIVATE_NAME','A','owner','ready',1)")
	if e != nil {
		t.Fatal(e)
	}
	p := &Panel{Store: db, Password: "private-panel-password-for-fixture", Username: "testbot", Backup: &backup.Service{Store: db, Dir: filepath.Join(root, "backups")}}
	request := func(method, path, origin, cookie string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, r)
		return w
	}
	origin := "http://127.0.0.1:8080"
	if w := request("POST", "/invite", origin, "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/login", "https://evil.test", "", url.Values{"password": {p.Password}}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := request("POST", "/login", origin, "", url.Values{"password": {p.Password}})
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	token := cookie.Name + "=" + cookie.Value
	w = request("GET", "/", "", token, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "SECRET_PRIVATE_NAME") || strings.Contains(w.Body.String(), p.Password) {
		t.Fatal("dashboard aggregates/privacy", w.Code, w.Body.String())
	}
	csrf := p.sessions[cookie.Value].csrf
	if w = request("POST", "/invite", origin, token, url.Values{"csrf": {"wrong"}}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/invite", origin, token, url.Values{"csrf": {csrf}}); w.Code != 200 || !strings.Contains(w.Body.String(), "https://t.me/testbot?start=") {
		t.Fatal(w.Code, w.Body.String())
	}
	var hash string
	db.DB.QueryRowContext(ctx, "SELECT token_hash FROM invitations WHERE kind='access'").Scan(&hash)
	w = request("POST", "/revoke", origin, token, url.Values{"csrf": {csrf}, "hash": {hash}})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var count int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM invitations WHERE revoked_at IS NOT NULL").Scan(&count)
	if count != 1 {
		t.Fatal("revocation failed")
	}
	if w = request("POST", "/backup", origin, token, url.Values{"csrf": {csrf}}); w.Code != 200 || !strings.Contains(w.Body.String(), "success") {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = http.MethodGet
}

func TestPanelTemplateIsRuntimeValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.html")
	if err := os.WriteFile(path, []byte(`changed {{.Health}}`), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := readView(path)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err = view.Execute(&output, Page{Health: "runtime"}); err != nil || output.String() != "changed runtime" {
		t.Fatal(output.String(), err)
	}
	if err = os.WriteFile(path, []byte(`{{if}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readView(path); err == nil {
		t.Fatal("invalid runtime template accepted")
	}
}
