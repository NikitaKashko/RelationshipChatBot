package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Router struct {
	Store    *store.Store
	Username string
	AI       *ai.Service
}

func button(text, data string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: text, CallbackData: data}}}}
}
func (r *Router) send(ctx context.Context, b *bot.Bot, actor int64, text string, markup any) {
	// Never print raw Telegram transport errors: request URLs contain credentials.
	chars := []rune(text)
	for len(chars) > 2000 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: string(chars[:2000])})
		chars = chars[2000:]
	}
	text = string(chars)
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
		return "Подтверждение устарело. Открой актуальную карточку или план и повтори изменение."
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
		text = "В каком часовом поясе ты живёшь? Для Москвы и Санкт-Петербурга нажми кнопку ниже. Можно написать «Москва» или свой часовой пояс, например Asia/Yekaterinburg."
		markup = button("Московское время · UTC+3", "timezone:moscow")
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
	_, err := store.UserFor(ctx, r.Store.DB, actor, false)
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
		var proposedBy sql.NullInt64
		var city, zone sql.NullString
		var version int64
		if err = r.Store.DB.QueryRowContext(ctx, "SELECT proposed_by,proposed_city,proposed_timezone,location_version FROM pairs WHERE id=?", pair).Scan(&proposedBy, &city, &zone, &version); err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		if proposedBy.Valid && proposedBy.Int64 != actor {
			r.draft(ctx, b, actor, "location_confirm", identity.LocationConfirmation{Version: version}, "Подтвердить город пары: "+city.String+"\nЧасовой пояс: "+zone.String)
			return
		}
		r.showMenu(ctx, b, actor, true)
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
	r.showMenu(ctx, b, actor, false)
}

func (r *Router) Handle(ctx context.Context, b *bot.Bot, update *models.Update) {
	if q := update.CallbackQuery; q != nil {
		if q.Message.Message == nil || q.Message.Message.Chat.Type != models.ChatTypePrivate || q.Message.Message.Chat.ID != q.From.ID {
			return
		}
		if q.Data == "timezone:moscow" {
			w, err := (&Enrollment{Store: r.Store}).Input(ctx, q.From.ID, "Europe/Moscow")
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, ShowAlert: err != nil, Text: func() string {
				if err != nil {
					return publicError(err)
				}
				return "Московское время выбрано"
			}()})
			if err == nil {
				r.wizard(ctx, b, q.From.ID, w)
			}
			return
		}
		if r.menuCallback(ctx, b, q) || r.aiCallback(ctx, b, q) || r.affectionCallback(ctx, b, q) || r.feedbackCallback(ctx, b, q) || r.collectionCallback(ctx, b, q) {
			return
		}
		if !strings.HasPrefix(q.Data, "save:") {
			return
		}
		svc := actions.Service{Store: r.Store}
		result, err := svc.Apply(ctx, q.From.ID, strings.TrimPrefix(q.Data, "save:"))
		text := "Сохранено"
		if err != nil {
			text = publicError(err)
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text, ShowAlert: err != nil})
		if err == nil {
			r.saved(ctx, b, q.From.ID, result)
		} else {
			r.duplicate(ctx, b, q.From.ID, err)
			var stale *planning.Stale
			if errors.As(err, &stale) {
				r.send(ctx, b, q.From.ID, "План уже изменился. Вот актуальная версия — подготовь правку заново.", nil)
				raw, _ := json.Marshal(map[string]int64{"plan": stale.ID})
				r.saved(ctx, b, q.From.ID, raw)
			}
		}
		return
	}
	m := update.Message
	if m == nil || m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.Chat.ID != m.From.ID {
		return
	}
	actor := m.From.ID
	text := strings.TrimSpace(m.Text)
	if command, ok := menuCommands[text]; ok {
		text = command
		copyMessage := *m
		copyMessage.Text = text
		m = &copyMessage
	}
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
	if text == "/settings" {
		r.send(ctx, b, actor, "Настройки 💛", navButtons("🤖 Помощник", "ai", "🌍 Город пары", "location", "💛 Имена для уведомлений", "names", "📋 Кандидаты свидания", "candidates", "🚫 Отменить приглашения партнёра", "cancelinvite", "🏠 Главное меню", "start"))
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
	if m.Voice != nil {
		r.send(ctx, b, actor, "Голосовые отложили. Напиши запрос текстом 💛", nil)
		return
	}
	if text == "/cancelinvite" {
		err := ids.CancelPartners(ctx, actor)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
		} else {
			r.send(ctx, b, actor, "Твои действующие приглашения в пару отменены. /invite — создать новое.", nil)
		}
		return
	}
	if r.collectionMessage(ctx, b, m) {
		return
	}
	if r.aiMessage(ctx, b, actor, text) {
		return
	}
	r.send(ctx, b, actor, "/start — меню, /places — идеи, /plans — свидания. Для помощника включи /ai и пиши обычным текстом.", nil)
}
