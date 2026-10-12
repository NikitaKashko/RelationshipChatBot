// Package dashboard serves owner aggregates and authenticated actions on loopback.
package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/backup"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

var view = mustLoadView()

func mustLoadView() *template.Template {
	path := filepath.Join(filepath.Dir(texts.Path()), "panel.html")
	parsed, err := readView(path)
	if err != nil {
		panic(err)
	}
	return parsed
}

func readView(path string) (*template.Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read panel template %q: %w", path, err)
	}
	parsed, err := template.New("panel").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse panel template %q: %w", path, err)
	}
	return parsed, nil
}

type session struct {
	csrf  string
	until time.Time
}
type Panel struct {
	Store                          *store.Store
	Backup                         *backup.Service
	Password, Username, HealthPath string
	mu                             sync.Mutex
	sessions                       map[string]session
	failures                       map[string]time.Time
}
type Metric struct {
	Label string
	Value int
}
type Day struct {
	Date                                  string
	Calls, Errors, Search, Tokens, Height int
}
type Invite struct{ Hash, Kind, Expires, Status string }
type Page struct {
	Login                                                bool
	CSRF, Message, Link, Health, LastBackup, LastSuccess string
	Metrics                                              []Metric
	Days                                                 []Day
	Invites                                              []Invite
}

func (p *Panel) Handler() http.Handler {
	return http.HandlerFunc(p.handle)
}
func (p *Panel) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.Host != "127.0.0.1:8080" && r.Host != "localhost:8080" {
		http.Error(w, texts.T("dashboard.http.invalid_host"), 403)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if r.Method == "POST" {
		if r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, texts.T("dashboard.http.invalid_origin"), 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, texts.T("dashboard.http.invalid_form"), 400)
			return
		}
	}
	if r.URL.Path == "/login" && r.Method == "POST" {
		p.mu.Lock()
		if p.failures == nil {
			p.failures = map[string]time.Time{}
		}
		if time.Now().Before(p.failures["login"]) {
			p.mu.Unlock()
			http.Error(w, texts.T("dashboard.http.retry_shortly"), 429)
			return
		}
		p.failures["login"] = time.Now().Add(time.Second)
		p.mu.Unlock()
		got, want := sha256.Sum256([]byte(r.Form.Get("password"))), sha256.Sum256([]byte(p.Password))
		if len(p.Password) < 24 || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, texts.T("internal.dashboard.dashboard.001"), 403)
			return
		}
		token, e := identity.RandomToken()
		if e != nil {
			http.Error(w, texts.T("dashboard.http.unavailable"), 503)
			return
		}
		csrf, e := identity.RandomToken()
		if e != nil {
			http.Error(w, texts.T("dashboard.http.unavailable"), 503)
			return
		}
		p.mu.Lock()
		if p.sessions == nil {
			p.sessions = map[string]session{}
		}
		for key, s := range p.sessions {
			if time.Now().After(s.until) {
				delete(p.sessions, key)
			}
		}
		p.sessions[token] = session{csrf, time.Now().Add(time.Hour)}
		p.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "owner_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
		http.Redirect(w, r, "/", 303)
		return
	}
	c, err := r.Cookie("owner_session")
	s := session{}
	if err == nil {
		p.mu.Lock()
		s = p.sessions[c.Value]
		p.mu.Unlock()
	}
	if s.csrf == "" || time.Now().After(s.until) {
		if r.Method == "POST" {
			http.Error(w, texts.T("dashboard.http.login_required"), 401)
			return
		}
		p.render(w, Page{Login: true})
		return
	}
	data := Page{CSRF: s.csrf}
	if r.Method == "POST" {
		if subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf")), []byte(s.csrf)) != 1 {
			http.Error(w, texts.T("dashboard.http.invalid_csrf"), 403)
			return
		}
		switch r.URL.Path {
		case "/invite", "/revoke":
			var actor int64
			if err = p.Store.DB.QueryRowContext(r.Context(), "SELECT id FROM users WHERE role='owner' AND status='ready'").Scan(&actor); err != nil {
				data.Message = texts.T("internal.dashboard.dashboard.002")
				break
			}
			ids := identity.Service{Store: p.Store}
			if r.URL.Path == "/invite" {
				var token string
				token, err = ids.Issue(r.Context(), actor, "access")
				if err == nil {
					data.Link = "https://t.me/" + p.Username + "?start=" + token
					data.Message = texts.T("internal.dashboard.dashboard.003")
				}
			} else {
				err = ids.RevokeHash(r.Context(), actor, r.Form.Get("hash"))
				if err == nil {
					data.Message = texts.T("internal.dashboard.dashboard.004")
				}
			}
		case "/backup":
			_, err = p.Backup.Run(r.Context())
			if err == nil {
				data.Message = texts.T("internal.dashboard.dashboard.005")
			}
		case "/logout":
			p.mu.Lock()
			delete(p.sessions, c.Value)
			p.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "owner_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", 303)
			return
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			data.Message = texts.T("internal.dashboard.dashboard.006")
		}
	} else if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if err = p.aggregate(r.Context(), &data); err != nil {
		http.Error(w, texts.T("dashboard.http.metrics_unavailable"), 503)
		return
	}
	p.render(w, data)
}
func (p *Panel) render(w http.ResponseWriter, data Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = view.Execute(w, data)
}
func (p *Panel) aggregate(ctx context.Context, d *Page) error {
	d.Health = texts.T("internal.dashboard.dashboard.007")
	if info, e := os.Stat(p.HealthPath); e == nil && time.Since(info.ModTime()) < 40*time.Second {
		d.Health = texts.T("internal.dashboard.dashboard.008")
	}
	metrics := []struct{ label, query string }{{texts.T("internal.dashboard.dashboard.009"), "SELECT count(*) FROM users WHERE status<>'deleted'"}, {texts.T("internal.dashboard.dashboard.010"), "SELECT count(*) FROM pairs WHERE status='active'"}, {texts.T("internal.dashboard.dashboard.011"), "SELECT count(*) FROM places WHERE archived=0"}, {texts.T("internal.dashboard.dashboard.012"), "SELECT count(*) FROM plans WHERE is_draft=0"}, {texts.T("internal.dashboard.dashboard.013"), "SELECT count(*) FROM plans WHERE is_draft=1"}, {texts.T("internal.dashboard.dashboard.014"), "SELECT count(*) FROM notifications WHERE status IN ('pending','sending')"}, {texts.T("internal.dashboard.dashboard.015"), "SELECT count(*) FROM notifications WHERE status IN ('failed','uncertain')"}, {texts.T("internal.dashboard.dashboard.016"), "SELECT count(*) FROM api_usage WHERE uncertain=1"}}
	for _, m := range metrics {
		v := 0
		if err := p.Store.DB.QueryRowContext(ctx, m.query).Scan(&v); err != nil {
			return err
		}
		d.Metrics = append(d.Metrics, Metric{m.label, v})
	}
	rows, e := p.Store.DB.QueryContext(ctx, "SELECT date(created_at,'unixepoch'),count(*),sum(CASE WHEN success=0 THEN 1 ELSE 0 END),coalesce(sum(search_requests),0),coalesce(sum(input_tokens+output_tokens),0) FROM api_usage WHERE created_at>=? GROUP BY 1 ORDER BY 1", time.Now().AddDate(0, 0, -14).Unix())
	if e != nil {
		return e
	}
	max := 1
	for rows.Next() {
		var day Day
		if e = rows.Scan(&day.Date, &day.Calls, &day.Errors, &day.Search, &day.Tokens); e != nil {
			rows.Close()
			return e
		}
		if day.Calls > max {
			max = day.Calls
		}
		d.Days = append(d.Days, day)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i := range d.Days {
		d.Days[i].Height = 12 + d.Days[i].Calls*88/max
	}
	d.LastBackup = texts.T("internal.dashboard.dashboard.017")
	d.LastSuccess = texts.T("internal.dashboard.dashboard.017")
	var status string
	var finished sql.NullFloat64
	e = p.Store.DB.QueryRowContext(ctx, "SELECT status,finished_at FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&status, &finished)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil {
		d.LastBackup = status
		if finished.Valid {
			d.LastBackup += " · " + time.Unix(int64(finished.Float64), 0).UTC().Format("02.01 15:04 UTC")
		}
	}
	e = p.Store.DB.QueryRowContext(ctx, "SELECT finished_at FROM backup_runs WHERE status='success' ORDER BY id DESC LIMIT 1").Scan(&finished)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil && finished.Valid {
		d.LastSuccess = time.Unix(int64(finished.Float64), 0).UTC().Format("02.01 15:04 UTC")
	}
	rows, e = p.Store.DB.QueryContext(ctx, "SELECT token_hash,kind,expires_at,claimed_by IS NOT NULL FROM invitations WHERE revoked_at IS NULL AND consumed_at IS NULL AND expires_at>? ORDER BY created_at DESC LIMIT 30", time.Now().Unix())
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var i Invite
		var expiry float64
		var claimed bool
		if e = rows.Scan(&i.Hash, &i.Kind, &expiry, &claimed); e != nil {
			return e
		}
		i.Expires = time.Unix(int64(expiry), 0).UTC().Format("02.01 15:04 UTC")
		i.Status = texts.T("internal.dashboard.dashboard.018")
		if claimed {
			i.Status = texts.T("internal.dashboard.dashboard.019")
		}
		d.Invites = append(d.Invites, i)
	}
	return rows.Err()
}
func Heartbeat(ctx context.Context, path string) error {
	if e := os.MkdirAll(filepath.Dir(path), 0770); e != nil {
		return e
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		if e := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0660); e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// Loopback must stay fixed to the destination permitted by the SSH policy.
func (p *Panel) Serve(ctx context.Context) error {
	if len(strings.TrimSpace(p.Password)) < 24 {
		return fmt.Errorf("PANEL_PASSWORD must contain at least 24 characters")
	}
	srv := &http.Server{Addr: "127.0.0.1:8080", Handler: p.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
