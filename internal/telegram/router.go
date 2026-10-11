package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Router struct {
	Store    *store.Store
	Username string
}

func button(text, data string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: text, CallbackData: data}}}}
}
func (r *Router) send(ctx context.Context, b *bot.Bot, actor int64, text string, markup any) {
	// Never print raw Telegram transport errors: request URLs contain credentials.
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: text, ReplyMarkup: markup})
}
func publicError(err error) string {
	if errors.Is(err, ErrAnswer) || errors.Is(err, ErrTimezone) {
		return err.Error()
	}
	if errors.Is(err, store.ErrDenied) {
		return "Это действие недоступно. Проверь приглашение и регистрацию."
	}
	if errors.Is(err, store.ErrConflict) {
		return "Это подтверждение устарело. Открой /start заново."
	}
	return "Не удалось выполнить действие. Попробуй ещё раз."
}

func (r *Router) wizard(ctx context.Context, b *bot.Bot, actor int64, w Wizard) {
	var text string
	var markup any
	switch w.Stage {
	case "city":
		text = "Привет 💛 В каком городе ты живёшь?"
	case "name":
		text = "Как подписывать твои знаки внимания? Например, Никита или зайка. /skip — оставить имя из Telegram."
	case "timezone":
		text = "Укажи часовой пояс, например Europe/Moscow. Его проверим перед сохранением."
	case "preview":
		e := Enrollment{Store: r.Store}
		draft, err := e.ProfileDraft(ctx, actor, w)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		text = fmt.Sprintf("Проверь профиль:\nГород: %s\nИмя: %s\nЧасовой пояс: %s", w.Profile.City, w.Profile.Name, w.Profile.Timezone)
		markup = button("Сохранить профиль", "save:"+draft)
	}
	r.send(ctx, b, actor, text, markup)
}

func (r *Router) home(ctx context.Context, b *bot.Bot, actor int64) {
	u, err := store.UserFor(ctx, r.Store.DB, actor, false)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	pair, err := store.PairFor(ctx, r.Store.DB, actor)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	if pair != 0 {
		r.send(ctx, b, actor, "Вы в паре 💛 Регистрация завершена. Остальные функции первого релиза ещё подключаются.", nil)
		return
	}
	e := Enrollment{Store: r.Store}
	hash, name, err := e.PendingPartner(ctx, actor)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	if hash != "" {
		raw, _ := json.Marshal(map[string]string{"hash": hash})
		svc := actions.Service{Store: r.Store}
		draft, err := svc.Prepare(ctx, actor, "pair_accept", raw)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.send(ctx, b, actor, "Создать пару с "+name+"?", button("Создать пару", "save:"+draft))
		return
	}
	r.send(ctx, b, actor, "Профиль готов. Твой код дружбы: "+u.Code+"\n/invite — получить ссылку для партнёра.", nil)
}

func (r *Router) Handle(ctx context.Context, b *bot.Bot, update *models.Update) {
	if q := update.CallbackQuery; q != nil {
		if q.Message.Message == nil || q.Message.Message.Chat.Type != models.ChatTypePrivate || q.Message.Message.Chat.ID != q.From.ID {
			return
		}
		if !strings.HasPrefix(q.Data, "save:") {
			return
		}
		svc := actions.Service{Store: r.Store}
		_, err := svc.Apply(ctx, q.From.ID, strings.TrimPrefix(q.Data, "save:"))
		text := "Сохранено"
		if err != nil {
			text = publicError(err)
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text, ShowAlert: err != nil})
		if err == nil {
			r.home(ctx, b, q.From.ID)
		}
		return
	}
	m := update.Message
	if m == nil || m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.Chat.ID != m.From.ID {
		return
	}
	actor := m.From.ID
	text := strings.TrimSpace(m.Text)
	ids := identity.Service{Store: r.Store}
	e := Enrollment{Store: r.Store}
	parts := strings.Fields(text)
	if len(parts) > 0 && strings.Split(parts[0], "@")[0] == "/start" {
		if len(parts) > 1 {
			if _, err := ids.Claim(ctx, actor, m.From.FirstName, parts[1]); err != nil {
				r.send(ctx, b, actor, publicError(err), nil)
				return
			}
		}
		u, err := store.UserFor(ctx, r.Store.DB, actor, true)
		if err != nil {
			r.send(ctx, b, actor, "Бот доступен по приглашению. Попроси ссылку у владельца или партнёра.", nil)
			return
		}
		if u.Status == "ready" {
			r.home(ctx, b, actor)
			return
		}
		w, err := e.Begin(ctx, actor)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.wizard(ctx, b, actor, w)
		return
	}
	u, err := store.UserFor(ctx, r.Store.DB, actor, true)
	if err != nil {
		r.send(ctx, b, actor, "Начни с приглашения и /start.", nil)
		return
	}
	if u.Status != "ready" {
		if text == "" {
			r.send(ctx, b, actor, "Пришли ответ текстом. Голосовые подключим следующим этапом.", nil)
			return
		}
		if strings.HasPrefix(text, "/") && text != "/skip" {
			r.send(ctx, b, actor, "Продолжи регистрацию через /start.", nil)
			return
		}
		w, err := e.Input(ctx, actor, text)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.wizard(ctx, b, actor, w)
		return
	}
	if text == "/invite" {
		token, err := ids.Issue(ctx, actor, "partner")
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.send(ctx, b, actor, "Пригласи партнёра: https://t.me/"+r.Username+"?start="+token+"\nСсылка одноразовая, действует 7 дней. Получатель подтвердит создание пары.", nil)
		return
	}
	r.send(ctx, b, actor, "Регистрация готова. /start — профиль и приглашение в пару. Остальные функции пока в разработке.", nil)
}
