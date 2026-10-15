package telegram

import (
	"context"
	"errors"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/affection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func affectionKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.affection.001"), CallbackData: "affection:kiss"}}, {{Text: texts.T("internal.telegram.affection.002"), CallbackData: "affection:thinking"}, {Text: texts.T("internal.telegram.affection.003"), CallbackData: "affection:hug"}}}}
}
func (r *Router) affectionCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	if !strings.HasPrefix(q.Data, "affection:") {
		return false
	}
	svc := affection.Service{Store: r.Store}
	err := svc.Click(ctx, q.From.ID, strings.TrimPrefix(q.Data, "affection:"))
	text := texts.T("internal.telegram.affection.004")
	if errors.Is(err, affection.ErrCooldown) {
		text = affection.ErrCooldown.Error()
	} else if err != nil {
		text = texts.T("internal.telegram.affection.005")
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text, ShowAlert: err != nil})
	return true
}
func (r *Router) affectionMessage(ctx context.Context, b *bot.Bot, actor int64, command, args string) bool {
	if command == "/attention" {
		r.send(ctx, b, actor, texts.T("internal.telegram.affection.006"), affectionKeyboard())
		return true
	}
	if command == "/names_reset" {
		r.draft(ctx, b, actor, "notification_names", affection.Names{}, texts.T("internal.telegram.affection.007"))
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
			nickname = texts.T("internal.telegram.affection.008")
		}
		r.send(ctx, b, actor, texts.T("internal.telegram.affection.009")+nickname+texts.T("internal.telegram.affection.010"), navButtons(texts.T("internal.telegram.affection.011"), "names_edit", texts.T("internal.telegram.affection.012"), "names_reset"))
		return true
	}
	if strings.Contains(args, "|") {
		r.send(ctx, b, actor, texts.T("internal.telegram.affection.013"), navButtons(texts.T("internal.telegram.affection.014"), "names"))
		return true
	}
	p := affection.Names{Partner: strings.TrimSpace(args)}
	r.draft(ctx, b, actor, "notification_names", p, texts.T("internal.telegram.affection.015")+p.Partner+texts.T("internal.telegram.affection.016"))
	return true
}
