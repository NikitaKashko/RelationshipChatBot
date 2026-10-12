package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type searchFixture struct {
	calls int
	last  ai.SearchRequest
}

func (s *searchFixture) Search(_ context.Context, r ai.SearchRequest) (ai.SearchCompletion, error) {
	s.calls++
	s.last = r
	return ai.SearchCompletion{Summary: "Место найдено по ссылке", Places: []ai.FoundPlace{{Card: collection.Input{Title: "Кафе", Categories: []string{"Покушать"}, Address: "Москва, улица 1", City: "Москва", Sources: []collection.Source{{URL: "https://example.com/cafe", RetrievedAt: 1}}}}}}, nil
}

type unusedModel struct{ calls int }

func (m *unusedModel) Complete(context.Context, []ai.Message, []any) (ai.Completion, error) {
	m.calls++
	return ai.Completion{}, nil
}

func TestTelegramBareLinkAndAddLinkShowReviewedCards(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "telegram.sqlite"))
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
	act := actions.Service{Store: db}
	raw, _ := json.Marshal(map[string]any{"kind": "ai", "enabled": true})
	id, err := act.Prepare(ctx, 1, "consent", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = act.Apply(ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	var messages []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Error(err)
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			text, _ := json.Marshal(r.FormValue("text"))
			body = map[string]json.RawMessage{"text": text, "reply_markup": json.RawMessage(r.FormValue("reply_markup"))}
		} else {
			json.NewDecoder(r.Body).Decode(&body)
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			messages = append(messages, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:fake-test-token", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	search := &searchFixture{}
	model := &unusedModel{}
	r := Router{Store: db, AI: &ai.Service{Store: db, Model: "test", Provider: model, Search: search}}
	for _, text := range []string{"https://example.com/cafe", "/add https://example.com/cafe"} {
		r.Handle(ctx, b, &models.Update{Message: &models.Message{From: &models.User{ID: 1}, Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}, Text: text}})
	}
	if search.calls != 2 || model.calls != 0 || search.last.Mode != "identify" {
		t.Fatal("link routed through redundant model or manual blank card")
	}
	var count int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM places").Scan(&count)
	if count != 0 {
		t.Fatal("card auto-saved")
	}
	cardButtons := 0
	firstSave := ""
	for _, msg := range messages {
		var markup models.InlineKeyboardMarkup
		json.Unmarshal(msg["reply_markup"], &markup)
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if button.Text == "➕ Добавить в коллекцию" && strings.HasPrefix(button.CallbackData, "save:") {
					cardButtons++
					if firstSave == "" {
						firstSave = button.CallbackData
					}
				}
			}
		}
	}
	if cardButtons != 2 {
		t.Fatal("review/save buttons missing", cardButtons)
	}
	r.Handle(ctx, b, &models.Update{CallbackQuery: &models.CallbackQuery{ID: "save-search", From: models.User{ID: 1, FirstName: "Owner"}, Data: firstSave, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 1, Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}}}}})
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM places").Scan(&count)
	if count != 1 {
		t.Fatal("reviewed card was not saved")
	}
	saved := messages[len(messages)-1]
	var savedText string
	_ = json.Unmarshal(saved["text"], &savedText)
	if !strings.Contains(savedText, "✅ Добавлено в коллекцию") || strings.Contains(savedText, "не проверен") || strings.Contains(savedText, "Не подтверждено:") {
		t.Fatal("saved card is unclear or exposes unknown fields", savedText)
	}
	var savedMarkup models.InlineKeyboardMarkup
	_ = json.Unmarshal(saved["reply_markup"], &savedMarkup)
	labels := []string{}
	for _, row := range savedMarkup.InlineKeyboard {
		for _, item := range row {
			labels = append(labels, item.Text)
		}
	}
	for _, want := range []string{"❤️ Хочу сходить", "🙅 Не интересно", "📝 В черновик свидания", "🗄 Убрать в архив"} {
		if !slices.Contains(labels, want) {
			t.Fatalf("missing clear action %q in %v", want, labels)
		}
	}
	r.Handle(ctx, b, &models.Update{Message: &models.Message{From: &models.User{ID: 1}, Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}, Text: "https://127.0.0.1/secret"}})
	if search.calls != 2 {
		t.Fatal("private URL sent to provider")
	}
}
