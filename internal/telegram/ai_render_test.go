package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func telegramBody(t *testing.T, request *http.Request) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Fatal(err)
		}
		body = map[string]json.RawMessage{}
		for _, key := range []string{"text", "parse_mode"} {
			value, _ := json.Marshal(request.FormValue(key))
			body[key] = value
		}
		for _, key := range []string{"reply_markup", "link_preview_options"} {
			body[key] = json.RawMessage(request.FormValue(key))
		}
		return body
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAIResultRendersMarkdownAndFiveIndependentSearchCards(t *testing.T) {
	var sent []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/sendMessage") {
			sent = append(sent, telegramBody(t, request))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:fake-test-token", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	result := ai.Result{Text: "**Нашёл пять вариантов**"}
	for i := 1; i <= 5; i++ {
		result.SearchCards = append(result.SearchCards, ai.SearchCard{
			Preview:   fmt.Sprintf("📍 Место %d", i),
			SaveToken: fmt.Sprintf("token-%d", i),
			MapURL:    fmt.Sprintf("https://yandex.ru/maps/?text=place-%d", i),
		})
	}
	(&Router{}).aiResult(context.Background(), b, 1, result, nil)
	if len(sent) != 6 {
		t.Fatalf("want header plus five cards, got %d", len(sent))
	}
	var parseMode string
	_ = json.Unmarshal(sent[0]["parse_mode"], &parseMode)
	if parseMode != string(models.ParseModeMarkdownV1) {
		t.Fatalf("AI markdown not enabled: %q", parseMode)
	}
	for i, message := range sent[1:] {
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal(message["reply_markup"], &markup); err != nil {
			t.Fatal(err)
		}
		if len(markup.InlineKeyboard) != 2 || markup.InlineKeyboard[0][0].Text != "➕ Добавить в коллекцию" || markup.InlineKeyboard[0][0].CallbackData != fmt.Sprintf("save:token-%d", i+1) {
			t.Fatalf("card %d has wrong independent save action: %+v", i+1, markup)
		}
		if !strings.Contains(markup.InlineKeyboard[1][0].URL, "yandex.ru/maps") {
			t.Fatalf("card %d lacks Yandex button", i+1)
		}
		if !strings.Contains(string(message["link_preview_options"]), "yandex.ru") {
			t.Fatalf("card %d lacks requested Telegram link preview: %s", i+1, message["link_preview_options"])
		}
	}
}

func TestAIMarkdownFallsBackToPlainText(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		body := telegramBody(t, request)
		var mode string
		_ = json.Unmarshal(body["parse_mode"], &mode)
		w.Header().Set("Content-Type", "application/json")
		if mode != "" {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := bot.New("123:fake-test-token", bot.WithSkipGetMe(), bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	(&Router{}).sendAI(context.Background(), b, 1, "сломанная _разметка", nil)
	if calls != 2 {
		t.Fatalf("plain-text fallback was not sent, calls=%d", calls)
	}
}
