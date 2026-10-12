package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestMissingPairTimezoneGuidesLocationProposal(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.ExecContext(ctx, `INSERT INTO users(id,name,notification_name,code,status,city,timezone,created_at) VALUES
		(1,'Никита','Никита','A','ready','Санкт-Петербург','Europe/Moscow',1),
		(2,'Партнёр','Партнёр','B','ready','Санкт-Петербург','Europe/Moscow',1);
		INSERT INTO pairs(id,created_at) VALUES(1,1);
		INSERT INTO pair_members(pair_id,user_id) VALUES(1,1),(1,2);`)
	if err != nil {
		t.Fatal(err)
	}
	action := actions.Service{Store: db}
	raw, _ := json.Marshal(planning.Input{Draft: true, Title: "Поездка в Выборг", Timezone: "Europe/Moscow"})
	id, err := action.Prepare(ctx, 1, "plan_save", raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := action.Apply(ctx, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct{ Plan int64 }
	if err = json.Unmarshal(result, &receipt); err != nil {
		t.Fatal(err)
	}

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(64 << 10)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			requests = append(requests, r.URL.Path+" "+r.FormValue("text")+" "+r.FormValue("reply_markup"))
		} else {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			encoded, _ := json.Marshal(body)
			requests = append(requests, r.URL.Path+" "+string(encoded))
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	router := Router{Store: db}
	message := func(text string) {
		router.Handle(ctx, b, &models.Update{Message: &models.Message{From: &models.User{ID: 1, FirstName: "Никита"}, Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}, Text: text}})
	}
	callback := func(data string) {
		router.Handle(ctx, b, &models.Update{CallbackQuery: &models.CallbackQuery{ID: data, From: models.User{ID: 1, FirstName: "Никита"}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}}}}})
	}

	callback("entry:date:" + strconvID(receipt.Plan))
	message("2026-10-18")
	var saveID string
	if err = db.DB.QueryRowContext(ctx, "SELECT id FROM drafts WHERE user_id=1 AND kind='plan_save' AND applied_at IS NULL ORDER BY created_at DESC LIMIT 1").Scan(&saveID); err != nil {
		t.Fatal(err)
	}
	callback("save:" + saveID)
	all := strings.Join(requests, "\n")
	if !strings.Contains(all, "Сначала настрой и подтверди город пары") || !strings.Contains(all, "menu:location") || strings.Contains(all, "Не удалось выполнить действие") {
		t.Fatal("missing timezone was not explained", all)
	}
	service := planning.Service{Store: db}
	saved, err := service.Get(ctx, db.DB, 1, receipt.Plan)
	if err != nil || !saved.Draft {
		t.Fatal("plan changed after failed approval", saved, err)
	}

	callback("menu:location")
	message("Санкт-Петербург")
	callback("entry:location_moscow")
	var locationID string
	if err = db.DB.QueryRowContext(ctx, "SELECT id FROM drafts WHERE user_id=1 AND kind='location_proposal' AND applied_at IS NULL ORDER BY created_at DESC LIMIT 1").Scan(&locationID); err != nil {
		t.Fatal("location wizard did not prepare proposal", err)
	}
	callback("save:" + locationID)
	var city, zone string
	var proposer int64
	if err = db.DB.QueryRowContext(ctx, "SELECT proposed_city,proposed_timezone,proposed_by FROM pairs WHERE id=1").Scan(&city, &zone, &proposer); err != nil {
		t.Fatal(err)
	}
	if city != "Санкт-Петербург" || zone != "Europe/Moscow" || proposer != 1 {
		t.Fatal("wrong location proposal", city, zone, proposer)
	}
	var notifications int
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM notifications WHERE kind='location_proposal' AND recipient_id=2").Scan(&notifications); err != nil || notifications != 1 {
		t.Fatal("partner was not notified", notifications, err)
	}
}
