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
			r.send(ctx, b, actor, "Для личного помощника нужно твоё согласие: нажми «🤖 Помощник». Для общей копилки и планов — согласие обоих. Ручные функции доступны всегда.", nil)
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
		label := "Сохранить"
		if strings.Contains(draft.Preview, "Черновик") {
			label = "📝 Сохранить черновик"
		}
		r.send(ctx, b, actor, draft.Preview, button(label, "save:"+draft.ID))
	}
	for _, card := range result.SearchCards {
		rows := [][]models.InlineKeyboardButton{{{Text: "➕ Добавить в коллекцию", CallbackData: "save:" + card.SaveToken}}}
		if card.MapURL != "" {
			rows = append(rows, []models.InlineKeyboardButton{{Text: "🗺 Открыть в Яндекс Картах", URL: card.MapURL}})
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
		r.send(ctx, b, actor, "Хочешь посмотреть ещё варианты?", button(result.SearchButton.Label, "ai_search:"+result.SearchButton.Token))
	}
}
func (r *Router) aiCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	search := strings.HasPrefix(q.Data, "ai_search:")
	if !search && !strings.HasPrefix(q.Data, "ai_retry:") {
		return false
	}
	answer := ""
	if search {
		answer = "Начал поиск"
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: answer})
	if r.AI == nil {
		r.send(ctx, b, q.From.ID, "ИИ ещё не настроен.", nil)
		return true
	}
	prefix := "ai_retry:"
	if search {
		prefix = "ai_search:"
		if q.Message.Message != nil {
			_, _ = b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: q.Message.Message.Chat.ID, MessageID: q.Message.Message.ID, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}})
		}
		r.send(ctx, b, q.From.ID, "Ищу места в интернете 🔎", nil)
	}
	result, err := r.AI.Retry(ctx, q.From.ID, strings.TrimPrefix(q.Data, prefix))
	if search && errors.Is(err, store.ErrConflict) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Этот поиск уже запущен"})
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
			r.send(ctx, b, actor, status+"\nНапиши, что хочется придумать или добавить.", navButtons("✨ Новый разговор", "newchat", "🧹 Очистить историю", "clearchat", "⏸ Отключить помощника", "ai_off", "🏠 Главное меню", "start"))
			return true
		}
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
