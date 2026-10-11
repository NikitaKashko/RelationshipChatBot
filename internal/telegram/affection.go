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
	text := "Знак внимания принят 💛 Доставлю с учётом тихих часов."
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
	if command != "/names" {
		return false
	}
	parts := strings.Split(args, "|")
	if len(parts) != 2 {
		r.send(ctx, b, actor, "/names моё имя | моё обращение к партнёру\nПустое обращение справа убирает замену имени.", nil)
		return true
	}
	p := affection.Names{Name: strings.TrimSpace(parts[0]), Partner: strings.TrimSpace(parts[1])}
	r.draft(ctx, b, actor, "notification_names", p, "Сохранить имена для уведомлений?\nТвоё имя: "+p.Name+"\nОбращение к партнёру: "+p.Partner)
	return true
}
