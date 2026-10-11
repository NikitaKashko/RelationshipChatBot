package telegram

import (
	"context"
	"encoding/json"
	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnabledAssistantSurvivesRestartAndMenuDoesNotAskAgain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ids := identity.Service{Store: db}
	token, _ := ids.Bootstrap(ctx)
	if _, err = ids.Claim(ctx, 1, "Owner", token); err != nil {
		t.Fatal(err)
	}
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 1, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
	}); err != nil {
		t.Fatal(err)
	}
	act := actions.Service{Store: db}
	draft, err := act.Prepare(ctx, 1, "consent", json.RawMessage(`{"kind":"ai","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = act.Apply(ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	db.DB.Close()
	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.ParseMultipartForm(65536)
			defer r.MultipartForm.RemoveAll()
			bodies = append(bodies, r.FormValue("text")+r.FormValue("reply_markup"))
		} else {
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			raw, _ := json.Marshal(body)
			bodies = append(bodies, string(raw))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	router := Router{Store: db}
	send := func(text string) {
		router.Handle(ctx, b, &models.Update{Message: &models.Message{From: &models.User{ID: 1}, Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}, Text: text}})
	}
	send("/start")
	send("🤖 Помощник")
	send("/ai")
	all := strings.Join(bodies, "\n")
	if !strings.Contains(all, "Помощник включён") || strings.Contains(all, "Разрешить DeepSeek") {
		t.Fatal("enabled state hidden or asked again", all)
	}
	if !strings.Contains(all, "💡 Копилка") || !strings.Contains(all, "is_persistent") {
		t.Fatal("persistent menu absent", all)
	}
	var n int
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM drafts WHERE kind='consent' AND applied_at IS NULL").Scan(&n); err != nil || n != 0 {
		t.Fatal("repeat consent drafts", n, err)
	}
	router.Handle(ctx, b, &models.Update{CallbackQuery: &models.CallbackQuery{ID: "cb", From: models.User{ID: 1}, Data: "menu:ai", Message: models.MaybeInaccessibleMessage{Message: &models.Message{Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}}}}})
}

func TestAssistantStatusExplainsMissingPartnerConsent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.ExecContext(ctx, `INSERT INTO users(id,name,code,status,created_at) VALUES(1,'A','A','ready',1),(2,'B','B','ready',1);
	INSERT INTO pairs(id,created_at) VALUES(1,1); INSERT INTO pair_members(pair_id,user_id) VALUES(1,1),(1,2);
	INSERT INTO consents(user_id,kind,enabled,updated_at) VALUES(1,'ai',1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	r := Router{Store: db}
	enabled, status, err := r.aiStatus(ctx, 1)
	if err != nil || !enabled || !strings.Contains(status, "партнёру тоже нужно") {
		t.Fatal(enabled, status, err)
	}
	_, err = db.DB.ExecContext(ctx, "INSERT INTO consents(user_id,kind,enabled,updated_at) VALUES(2,'ai',1,1)")
	if err != nil {
		t.Fatal(err)
	}
	_, status, err = r.aiStatus(ctx, 1)
	if err != nil || strings.Contains(status, "партнёру тоже нужно") {
		t.Fatal(status, err)
	}
}
