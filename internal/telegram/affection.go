package telegram

import (
	"context"
	"errors"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/affection"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func affectionKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Хочу внимания", CallbackData: "affection:attention"}}, {{Text: "Думаю о тебе", CallbackData: "affection:thinking"}, {Text: "Хочу обнять", CallbackData: "affection:hug"}}}}
}
func (r *Router) affectionCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	if !strings.HasPrefix(q.Data, "affection:") {
		return false
	}
	svc := affection.Service{Store: r.Store}
	err := svc.Click(ctx, q.From.ID, strings.TrimPrefix(q.Data, "affection:"))
	text := "Знак внимания принят 💛 Доставлю сразу, даже в тихие часы."
	if errors.Is(err, affection.ErrCooldown) {
		text = affection.ErrCooldown.Error()
	} else if err != nil {
		text = "Для знаков внимания нужна активная пара."
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text, ShowAlert: err != nil})
	return true
}
func (r *Router) affectionMessage(ctx context.Context, b *bot.Bot, actor int64, command, args string) bool {
	if command == "/attention" {
		r.send(ctx, b, actor, "Какой знак внимания отправим? 💛", affectionKeyboard())
		return true
	}
	if command == "/names_reset" {
		r.draft(ctx, b, actor, "notification_names", affection.Names{}, "Вернуть имя партнёра из Telegram в твоих уведомлениях?")
		return true
	}
	if command != "/names" {
		return false
	}
	if args == "" {
		var nickname string
		if err := r.Store.DB.QueryRowContext(ctx, "SELECT coalesce(partner_nickname,'') FROM users WHERE id=?", actor).Scan(&nickname); err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		if nickname == "" {
			nickname = "Имя из Telegram"
		}
		r.send(ctx, b, actor, "Как подписывать партнёра в уведомлениях, которые получаешь ты?\nСейчас: "+nickname+"\nОтправитель это имя не задаёт.", navButtons("✏️ Задать имя партнёра", "names_edit", "↩️ Имя из Telegram", "names_reset"))
		return true
	}
	if strings.Contains(args, "|") {
		r.send(ctx, b, actor, "Теперь задаётся только имя партнёра для тебя. Открой настройки имени.", navButtons("Имя партнёра", "names"))
		return true
	}
	p := affection.Names{Partner: strings.TrimSpace(args)}
	r.draft(ctx, b, actor, "notification_names", p, "В твоих уведомлениях партнёр будет подписан: "+p.Partner+". Сохранить?")
	return true
}
