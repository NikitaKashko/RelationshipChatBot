package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestReviewButtonsConfirmAndFeedbackUsesExistingFact(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.ExecContext(ctx, `INSERT INTO users(id,name,code,status,timezone,created_at) VALUES(1,'A','A','ready','UTC',1),(2,'B','B','ready','UTC',1);
 INSERT INTO pairs(id,city,timezone,created_at) VALUES(1,'Москва','UTC',1);
 INSERT INTO pair_members(pair_id,user_id) VALUES(1,1),(1,2);
 INSERT INTO places(id,author_id,pair_id,title,created_at) VALUES(1,1,1,'Кафе',1);
 INSERT INTO plans(id,pair_id,author_id,title,start_at,end_at,timezone,created_at) VALUES(1,1,1,'Вечер',1,2,'UTC',1);
 INSERT INTO visits(id,pair_id,plan_id,marked_by,visit_date,visited_at,created_at) VALUES(1,1,1,1,'2026-10-01',1,1);
 INSERT INTO visit_items(visit_id,place_id) VALUES(1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.ParseMultipartForm(1024 * 1024)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			body = map[string]any{"text": r.FormValue("text")}
		} else {
			json.NewDecoder(r.Body).Decode(&body)
		}
		if s, ok := body["text"].(string); ok {
			mu.Lock()
			texts = append(texts, s)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":2,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	r := Router{Store: db}
	r.feedbackCallback(ctx, b, &models.CallbackQuery{ID: "1", From: models.User{ID: 2}, Data: "rate:1:1:liked"})
	r.feedbackCallback(ctx, b, &models.CallbackQuery{ID: "2", From: models.User{ID: 2}, Data: "again:1:1:liked:never"})
	var count int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM reviews").Scan(&count)
	if count != 0 {
		t.Fatal("buttons wrote review before Save")
	}
	var draft string
	if err = db.DB.QueryRowContext(ctx, "SELECT id FROM drafts WHERE kind='review' AND user_id=2").Scan(&draft); err != nil {
		t.Fatal(err)
	}
	act := actions.Service{Store: db}
	if _, err = act.Apply(ctx, 2, draft); err != nil {
		t.Fatal(err)
	}
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM reviews WHERE user_id=2 AND again='never'").Scan(&count)
	if count != 1 {
		t.Fatal("confirmed review missing")
	}
	send := r.NotificationSender(b)
	if _, err = send(ctx, notifications.Message{Pair: 1, Recipient: 2, Kind: "feedback", Payload: json.RawMessage(`{"plan_id":1}`)}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(texts) == 0 {
		mu.Unlock()
		t.Fatal("fake API did not capture message")
	}
	last := texts[len(texts)-1]
	mu.Unlock()
	if !strings.Contains(last, "только твоё впечатление") || strings.Contains(last, "/notwent") {
		t.Fatal("asked second partner for visit facts again", last)
	}
}
