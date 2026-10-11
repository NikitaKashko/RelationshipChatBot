package telegram

import (
	"context"
	"errors"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (r *Router) aiResult(ctx context.Context, b *bot.Bot, actor int64, result ai.Result, err error) {
	if err != nil {
		if errors.Is(err, store.ErrDenied) {
			r.send(ctx, b, actor, "Для ИИ нужно согласие твоё и партнёра: /ai. Ручные функции доступны всегда.", nil)
		} else {
			r.send(ctx, b, actor, publicError(err), nil)
		}
		return
	}
	var markup any
	if result.Retry != "" {
		markup = button("Повторить", "ai_retry:"+result.Retry)
	}
	if result.Text != "" {
		r.send(ctx, b, actor, result.Text, markup)
	}
	seen := map[int64]bool{}
	for _, id := range result.Places {
		if !seen[id] {
			seen[id] = true
			r.card(ctx, b, actor, id)
		}
	}
	for _, draft := range result.Drafts {
		r.send(ctx, b, actor, draft.Preview, button("Сохранить", "save:"+draft.ID))
	}
	for _, card := range result.SearchCards {
		rows := [][]models.InlineKeyboardButton{{{Text: "В идеи", CallbackData: "save:" + card.Ideas}}}
		if card.Candidates != "" {
			rows[0] = append(rows[0], models.InlineKeyboardButton{Text: "В кандидаты свидания", CallbackData: "save:" + card.Candidates})
		}
		r.send(ctx, b, actor, card.Preview, models.InlineKeyboardMarkup{InlineKeyboard: rows})
	}
	if result.SearchButton != nil {
		r.send(ctx, b, actor, "Поиск запускается по твоему запросу.", button(result.SearchButton.Label, "ai_retry:"+result.SearchButton.Token))
	}
}
func (r *Router) aiCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	if !strings.HasPrefix(q.Data, "ai_retry:") {
		return false
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if r.AI == nil {
		r.send(ctx, b, q.From.ID, "ИИ ещё не настроен.", nil)
		return true
	}
	result, err := r.AI.Retry(ctx, q.From.ID, strings.TrimPrefix(q.Data, "ai_retry:"))
	r.aiResult(ctx, b, q.From.ID, result, err)
	return true
}
func (r *Router) aiMessage(ctx context.Context, b *bot.Bot, actor int64, text string) bool {
	switch text {
	case "/ai":
		r.draft(ctx, b, actor, "consent", map[string]any{"kind": "ai", "enabled": true}, "Разрешить DeepSeek использовать твой текст, необходимые поля мест, даты планов и подтверждённые предпочтения? Для данных пары требуется согласие обоих. Имена для уведомлений, Telegram ID и фото не передаём. История личная, 30 дней. Для отключения — /ai_off.")
		return true
	case "/ai_off":
		r.draft(ctx, b, actor, "consent", map[string]any{"kind": "ai", "enabled": false}, "Отключить передачу данных DeepSeek и сделать прежние ИИ-черновики недействительными? Ручные функции сохранятся.")
		return true
	case "/newchat":
		if r.AI == nil {
			r.send(ctx, b, actor, "ИИ ещё не настроен.", nil)
			return true
		}
		_, err := r.AI.New(ctx, actor)
		if err != nil {
			r.aiResult(ctx, b, actor, ai.Result{}, err)
		} else {
			r.send(ctx, b, actor, "Начали новый разговор. Места, планы и подтверждённые предпочтения сохранены.", nil)
		}
		return true
	case "/clearchat":
		if r.AI == nil {
			r.send(ctx, b, actor, "ИИ ещё не настроен.", nil)
			return true
		}
		err := r.AI.Clear(ctx, actor)
		if err != nil {
			r.aiResult(ctx, b, actor, ai.Result{}, err)
		} else {
			r.send(ctx, b, actor, "Личная история ИИ очищена. Сохранённые данные остались.", nil)
		}
		return true
	}
	if text == "" || strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "/search ") {
		return false
	}
	if r.AI == nil || r.AI.Provider == nil {
		r.send(ctx, b, actor, "ИИ пока не настроен. /places — копилка, /plans — планы.", nil)
		return true
	}
	r.send(ctx, b, actor, "Сейчас посмотрю 💛", nil)
	result, err := r.AI.Converse(ctx, actor, text)
	r.aiResult(ctx, b, actor, result, err)
	return true
}
