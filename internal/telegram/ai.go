package telegram

import (
	"context"
	"errors"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (r *Router) aiResult(ctx context.Context, b *bot.Bot, actor int64, result ai.Result, err error) {
	if err != nil {
		if errors.Is(err, store.ErrDenied) {
			r.send(ctx, b, actor, texts.T("internal.telegram.ai.001"), nil)
		} else {
			r.send(ctx, b, actor, publicError(err), nil)
		}
		return
	}
	var markup any
	if result.Retry != "" {
		markup = button(texts.T("internal.telegram.ai.002"), "ai_retry:"+result.Retry)
	}
	if result.Text != "" {
		r.sendAI(ctx, b, actor, result.Text, markup)
	}
	seen := map[int64]bool{}
	for _, id := range result.Places {
		if !seen[id] {
			seen[id] = true
			r.card(ctx, b, actor, id)
		}
	}
	for _, draft := range result.Drafts {
		label := texts.T("internal.telegram.ai.003")
		if strings.Contains(draft.Preview, texts.T("internal.telegram.ai.004")) {
			label = texts.T("internal.telegram.ai.005")
		}
		r.send(ctx, b, actor, draft.Preview, button(label, "save:"+draft.ID))
	}
	for _, card := range result.SearchCards {
		rows := [][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.ai.006"), CallbackData: "save:" + card.SaveToken}}}
		if card.MapURL != "" {
			rows = append(rows, []models.InlineKeyboardButton{{Text: texts.T("internal.telegram.ai.007"), URL: card.MapURL}})
		}
		large, above := true, true
		params := &bot.SendMessageParams{ChatID: actor, Text: card.Preview, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: rows}}
		if card.MapURL != "" {
			params.LinkPreviewOptions = &models.LinkPreviewOptions{URL: &card.MapURL, PreferLargeMedia: &large, ShowAboveText: &above}
		}
		if _, sendErr := b.SendMessage(ctx, params); sendErr != nil {
			r.send(ctx, b, actor, card.Preview, models.InlineKeyboardMarkup{InlineKeyboard: rows})
		}
	}
	if result.SearchButton != nil {
		r.send(ctx, b, actor, texts.T("internal.telegram.ai.008"), button(result.SearchButton.Label, "ai_search:"+result.SearchButton.Token))
	}
}
func (r *Router) aiCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	search := strings.HasPrefix(q.Data, "ai_search:")
	if !search && !strings.HasPrefix(q.Data, "ai_retry:") {
		return false
	}
	answer := ""
	if search {
		answer = texts.T("internal.telegram.ai.009")
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: answer})
	if r.AI == nil {
		r.send(ctx, b, q.From.ID, texts.T("internal.telegram.ai.010"), nil)
		return true
	}
	prefix := "ai_retry:"
	if search {
		prefix = "ai_search:"
		if q.Message.Message != nil {
			_, _ = b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: q.Message.Message.Chat.ID, MessageID: q.Message.Message.ID, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}})
		}
		r.send(ctx, b, q.From.ID, texts.T("internal.telegram.ai.011"), nil)
	}
	result, err := r.AI.Retry(ctx, q.From.ID, strings.TrimPrefix(q.Data, prefix))
	if search && errors.Is(err, store.ErrConflict) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: texts.T("internal.telegram.ai.012")})
		return true
	}
	r.aiResult(ctx, b, q.From.ID, result, err)
	return true
}
func (r *Router) aiMessage(ctx context.Context, b *bot.Bot, actor int64, text string) bool {
	switch text {
	case "/ai":
		enabled, status, err := r.aiStatus(ctx, actor)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		if enabled {
			r.send(ctx, b, actor, status+texts.T("internal.telegram.ai.013"), navButtons(texts.T("internal.telegram.ai.014"), "newchat", texts.T("internal.telegram.ai.015"), "clearchat", texts.T("internal.telegram.ai.016"), "ai_off", texts.T("internal.telegram.ai.017"), "start"))
			return true
		}
		r.draft(ctx, b, actor, "consent", map[string]any{"kind": "ai", "enabled": true}, texts.T("internal.telegram.ai.018"))
		return true
	case "/ai_off":
		r.draft(ctx, b, actor, "consent", map[string]any{"kind": "ai", "enabled": false}, texts.T("internal.telegram.ai.019"))
		return true
	case "/newchat":
		if r.AI == nil {
			r.send(ctx, b, actor, texts.T("internal.telegram.ai.010"), nil)
			return true
		}
		_, err := r.AI.New(ctx, actor)
		if err != nil {
			r.aiResult(ctx, b, actor, ai.Result{}, err)
		} else {
			r.send(ctx, b, actor, texts.T("internal.telegram.ai.020"), nil)
		}
		return true
	case "/clearchat":
		if r.AI == nil {
			r.send(ctx, b, actor, texts.T("internal.telegram.ai.010"), nil)
			return true
		}
		err := r.AI.Clear(ctx, actor)
		if err != nil {
			r.aiResult(ctx, b, actor, ai.Result{}, err)
		} else {
			r.send(ctx, b, actor, texts.T("internal.telegram.ai.021"), nil)
		}
		return true
	}
	if text == "" || strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "/search ") {
		return false
	}
	if r.AI == nil || r.AI.Provider == nil {
		r.send(ctx, b, actor, texts.T("internal.telegram.ai.022"), nil)
		return true
	}
	r.send(ctx, b, actor, texts.T("internal.telegram.ai.023"), nil)
	result, err := r.AI.Converse(ctx, actor, text)
	r.aiResult(ctx, b, actor, result, err)
	return true
}
