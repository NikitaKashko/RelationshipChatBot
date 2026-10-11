package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var menuCommands = map[string]string{
	"🏠 Меню": "/start", "💡 Копилка": "/places", "📅 Свидания": "/plans",
	"🤖 Помощник": "/ai", "💌 Внимание": "/attention", "🔗 Пригласить партнёра": "/invite", "⚙️ Настройки": "/settings",
	"🎟 Пригласить пользователя": "/accessinvite",
}

func mainKeyboard(owner bool) models.ReplyKeyboardMarkup {
	keyboard := models.ReplyKeyboardMarkup{Keyboard: [][]models.KeyboardButton{
		{{Text: "💡 Копилка"}, {Text: "📅 Свидания"}}, {{Text: "🤖 Помощник"}, {Text: "💌 Внимание"}},
		{{Text: "🔗 Пригласить партнёра"}, {Text: "⚙️ Настройки"}}, {{Text: "🏠 Меню"}},
	}, ResizeKeyboard: true, IsPersistent: true}
	if owner {
		keyboard.Keyboard = append(keyboard.Keyboard, []models.KeyboardButton{{Text: "🎟 Пригласить пользователя"}})
	}
	return keyboard
}
func navButtons(items ...string) models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{}
	for i := 0; i+1 < len(items); i += 2 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: items[i], CallbackData: "menu:" + items[i+1]}})
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
func (r *Router) aiStatus(ctx context.Context, actor int64) (bool, string, error) {
	var enabled int
	err := r.Store.DB.QueryRowContext(ctx, "SELECT enabled FROM consents WHERE user_id=? AND kind='ai'", actor).Scan(&enabled)
	if err != nil && err != sql.ErrNoRows {
		return false, "", err
	}
	if enabled != 1 {
		return false, "Помощник выключен", nil
	}
	pair, err := store.PairFor(ctx, r.Store.DB, actor)
	if err != nil {
		return false, "", err
	}
	status := "Помощник включён ✅"
	if pair != 0 {
		var other int
		err = r.Store.DB.QueryRowContext(ctx, "SELECT coalesce(c.enabled,0) FROM pair_members m LEFT JOIN consents c ON c.user_id=m.user_id AND c.kind='ai' WHERE m.pair_id=? AND m.active=1 AND m.user_id<>?", pair, actor).Scan(&other)
		if err != nil {
			return false, "", err
		}
		if other != 1 {
			status += "\nЛичный ИИ и поиск новых мест доступны. Подбор из общей копилки скрыт, пока партнёр не включит ИИ. Копилка вручную доступна всегда."
		}
	}
	return true, status, nil
}
func (r *Router) showMenu(ctx context.Context, b *bot.Bot, actor int64, paired bool) {
	_, status, err := r.aiStatus(ctx, actor)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	title := "Профиль готов 💛"
	if paired {
		title = "Вы в паре 💛"
	}
	u, err := store.UserFor(ctx, r.Store.DB, actor, false)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	r.send(ctx, b, actor, fmt.Sprintf("%s\n%s\n\nВыбирай действие кнопками ниже или пиши помощнику обычным текстом.", title, status), mainKeyboard(u.Role == "owner"))
}
func (r *Router) menuCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	commands := map[string]string{"ai": "/ai", "ai_off": "/ai_off", "newchat": "/newchat", "clearchat": "/clearchat", "start": "/start", "invite": "/invite", "cancelinvite": "/cancelinvite", "location": "/location", "names": "/names", "candidates": "/drafts", "drafts": "/drafts", "add": "/add", "add_manual": "/add_manual", "add_ai": "/add_ai", "draft_new": "/draft_new", "plan_new": "/plan_new", "date_ai": "/date_ai", "skip": "/skip", "entry_cancel": "/entry_cancel", "accessinvite": "/accessinvite", "quiet": "/quiet", "quiet_custom": "/quiet_custom", "quiet_cancel": "/quiet_cancel", "quiet_default": "/quiet 23:00-09:00", "quiet_late": "/quiet 00:00-10:00", "quiet_off": "/quiet 00:00-00:00"}
	if len(q.Data) < 5 || q.Data[:5] != "menu:" {
		return false
	}
	command, ok := commands[q.Data[5:]]
	if !ok {
		return true
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	r.Handle(ctx, b, &models.Update{Message: &models.Message{From: &q.From, Chat: q.Message.Message.Chat, Text: command}})
	return true
}
