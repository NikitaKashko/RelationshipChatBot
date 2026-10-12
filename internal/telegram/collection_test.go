package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestTelegramCardCoverAndTwoPhotosOnDemand(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	ids := identity.Service{Store: db}
	invite, _ := ids.Bootstrap(ctx)
	ids.Claim(ctx, 1, "Owner", invite)
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 1, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
	}); err != nil {
		t.Fatal(err)
	}
	svc := actions.Service{Store: db}
	raw, _ := json.Marshal(collection.Input{Title: "Кафе", Categories: []string{"Покушать"}, Photos: []string{"file_cover", "file_second", "file_third"}})
	draft, err := svc.Prepare(ctx, 1, "place_create", raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Apply(ctx, 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Place int64 }
	json.Unmarshal(result, &out)
	var mu sync.Mutex
	var photos []string
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.ParseMultipartForm(1024 * 1024)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			body = map[string]any{"photo": r.FormValue("photo"), "text": r.FormValue("text")}
		} else {
			json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			if id, ok := body["photo"].(string); ok {
				photos = append(photos, id)
			}
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if text, ok := body["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:fake-test-token", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	router := Router{Store: db}
	router.card(ctx, b, 1, out.Place)
	if len(photos) != 1 || photos[0] != "file_cover" {
		t.Fatal("cover not sent alone", photos)
	}
	if len(texts) != 1 || strings.Contains(texts[0], "Бюджет не проверен") || strings.Contains(texts[0], "Часы работы не проверены") {
		t.Fatal("unknown fields shown", texts)
	}
	router.collectionCallback(ctx, b, &models.CallbackQuery{ID: "test", From: models.User{ID: 1}, Data: "photos:" + strconvID(out.Place)})
	if len(photos) != 3 || photos[1] != "file_second" || photos[2] != "file_third" {
		t.Fatal("extra photos not on demand", photos)
	}
	router.Handle(ctx, b, &models.Update{Message: &models.Message{From: &models.User{ID: 1}, Chat: models.Chat{ID: -1, Type: models.ChatTypeGroup}, Text: "/add Group idea"}})
	var count int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM drafts WHERE kind='place_create'").Scan(&count)
	if count != 1 {
		t.Fatal("group update created draft")
	}
	raw, _ = json.Marshal(collection.Input{Title: "Пикник", Categories: []string{"Погулять"}})
	draft, err = svc.Prepare(ctx, 1, "place_create", raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err = svc.Apply(ctx, 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(result, &out)
	router.card(ctx, b, 1, out.Place)
	if len(photos) != 3 || len(texts) != 2 || strings.Contains(texts[1], "Адрес не указан") {
		t.Fatal("text-only card failed", photos, texts)
	}
}

func strconvID(v int64) string { return fmt.Sprint(v) }
