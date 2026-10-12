package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/config"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/telegram"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type roundFunc func(*http.Request) (*http.Response, error)

func (f roundFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSDKTransportRewritesOnlyVirtualRecipients(t *testing.T) {
	sent := 0
	client := &http.Client{Transport: transport{owner: 999, next: roundFunc(func(req *http.Request) (*http.Response, error) {
		sent++
		if strings.HasSuffix(req.URL.Path, "getMe") {
			return response(`{"ok":true,"result":{"id":100,"is_bot":true,"first_name":"Test","username":"test_bot"}}`), nil
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer req.MultipartForm.RemoveAll()
		if req.FormValue("chat_id") != "999" || !strings.HasPrefix(req.FormValue("text"), "[ТЕСТ · Б]") {
			t.Fatal("recipient not mapped")
		}
		if !strings.Contains(req.FormValue("reply_markup"), "sim:2:save:abc") {
			t.Fatal("callback role missing")
		}
		return response(`{"ok":true,"result":{"message_id":10,"date":1,"chat":{"id":999,"type":"private"},"text":"ok"}}`), nil
	})}}
	b, err := bot.New("fake-token", bot.WithSkipGetMe(), bot.WithHTTPClient(60, client), bot.WithErrorsHandler(func(error) {}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: 2, Text: "Привет", ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Save", CallbackData: "save:abc"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: 123, Text: "Denied"}); err == nil {
		t.Fatal("foreign recipient allowed")
	}
	if sent != 2 {
		t.Fatal("foreign request sent")
	}
}
func response(raw string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}
}

func TestRoleMappingRejectsForeignAndUsesCallbackOrigin(t *testing.T) {
	s := simulator{owner: 999}
	s.role.Store(1)
	message := func(uid int64, text string) *models.Update {
		return &models.Update{Message: &models.Message{From: &models.User{ID: uid, FirstName: "Никита", LastName: "К"}, Chat: models.Chat{ID: uid, Type: models.ChatTypePrivate}, Text: text}}
	}
	if _, ok := s.mapUpdate(message(222, "/role_b")); ok || s.role.Load() != 1 {
		t.Fatal("foreign account switched role")
	}
	u := message(999, "/role_b")
	if role, ok := s.mapUpdate(u); !ok || role != 2 || u.Message.From.ID != 2 || u.Message.Chat.ID != 2 {
		t.Fatal("role mapping")
	}
	q := &models.CallbackQuery{From: models.User{ID: 999, FirstName: "Никита", LastName: "К"}, Data: "sim:1:save:abc", Message: models.MaybeInaccessibleMessage{Message: &models.Message{Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate}}}}
	if role, ok := s.mapUpdate(&models.Update{CallbackQuery: q}); !ok || role != 1 || q.Data != "save:abc" || q.From.LastName != "К" {
		t.Fatal("callback changed owner role")
	}
	q.From.ID = 999
	q.Message.Message.Chat.ID = 999
	q.Data = "save:abc"
	if _, ok := s.mapUpdate(&models.Update{CallbackQuery: q}); ok {
		t.Fatal("unmarked callback allowed")
	}
	group := message(999, "hello")
	group.Message.Chat.Type = models.ChatTypeGroup
	if _, ok := s.mapUpdate(group); ok {
		t.Fatal("group allowed")
	}
}

func TestSeparateConfigurationAndSeed(t *testing.T) {
	root := t.TempDir()
	c := config.Config{TestBotToken: "test", BotToken: "prod", TestOwnerID: 999, TestDatabasePath: filepath.Join(root, "sandbox.sqlite"), DatabasePath: filepath.Join(root, "release.sqlite")}
	if err := validate(c); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []config.Config{
		{TestBotToken: "prod", BotToken: "prod", TestOwnerID: 999, TestDatabasePath: c.TestDatabasePath, DatabasePath: c.DatabasePath},
		{TestBotToken: "test", TestOwnerID: 1, TestDatabasePath: c.TestDatabasePath, DatabasePath: c.DatabasePath},
		{TestBotToken: "test", TestOwnerID: 999, TestDatabasePath: c.TestDatabasePath, DatabasePath: c.TestDatabasePath},
	} {
		if validate(bad) == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	ctx := context.Background()
	db, err := store.Open(ctx, c.TestDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.DB.Close() }()
	if err = seed(ctx, db, 999); err != nil {
		t.Fatal(err)
	}
	if err = db.DB.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, c.TestDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = seed(ctx, db, 999); err != nil {
		t.Fatal("restart", err)
	}
	if seed(ctx, db, 888) == nil {
		t.Fatal("changed owner accepted")
	}
	var users, consents int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&users)
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM consents WHERE enabled=1").Scan(&consents)
	pair, err := store.PairFor(ctx, db.DB, 1)
	if err != nil || pair == 0 || users != 2 || consents != 0 {
		t.Fatal("seed not isolated", err)
	}
	db.DB.ExecContext(ctx, "DROP TABLE sandbox_identity")
	if seed(ctx, db, 999) == nil {
		t.Fatal("unmarked populated database accepted")
	}
}

func TestRoleStartAndSwitchRenderRealMenu(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sandbox.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if err = seed(ctx, db, 999); err != nil {
		t.Fatal(err)
	}
	r := telegram.Router{Store: db}
	s := simulator{owner: 999}
	seen := 0
	client := &http.Client{Transport: transport{owner: 999, next: roundFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer req.MultipartForm.RemoveAll()
		if !strings.Contains(req.FormValue("reply_markup"), "🧪 Роль А") || !strings.Contains(req.FormValue("reply_markup"), "🧪 Роль Б") {
			t.Fatal("missing actual role menu", req.FormValue("text"))
		}
		seen++
		return response(`{"ok":true,"result":{"message_id":10,"date":1,"chat":{"id":999,"type":"private"}}}`), nil
	})}}
	b, err := bot.New("fake", bot.WithSkipGetMe(), bot.WithHTTPClient(60, client), bot.WithErrorsHandler(func(error) {}))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/start", "🧪 Роль Б", "🧪 Роль А"} {
		u := &models.Update{Message: &models.Message{From: &models.User{ID: 999, FirstName: "Никита"}, Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate}, Text: text}}
		if _, ok := s.mapUpdate(u); !ok {
			t.Fatal("owner rejected")
		}
		r.Handle(ctx, b, u)
	}
	if seen != 3 {
		t.Fatal("router did not render menus", seen)
	}
}

func TestMultipartPhotoAndRoleKeyboard(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("chat_id", "1")
	w.WriteField("caption", "Фото")
	w.WriteField("reply_markup", `{"keyboard":[[{"text":"Меню"}]],"resize_keyboard":true}`)
	part, _ := w.CreateFormFile("photo", "example.jpg")
	part.Write([]byte("image fixture"))
	w.Close()
	req, _ := http.NewRequest("POST", "https://api.telegram.org/bottest/sendPhoto", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	tr := transport{owner: 999, next: roundFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("caption") != "[ТЕСТ · А] Фото" {
			t.Fatal("photo caption")
		}
		f, _, err := r.FormFile("photo")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		if string(data) != "image fixture" {
			t.Fatal("lost photo")
		}
		var keyboard map[string]any
		json.Unmarshal([]byte(r.FormValue("reply_markup")), &keyboard)
		if len(keyboard["keyboard"].([]any)) != 2 {
			t.Fatal("role buttons absent")
		}
		return response(`{}`), nil
	})}
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
}
