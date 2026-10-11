package telegram

import (
	"context"
	"fmt"
	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/go-telegram/bot"
	"strings"
)

func (r *Router) showQuiet(ctx context.Context, b *bot.Bot, actor int64) {
	var start, end, zone string
	if err := r.Store.DB.QueryRowContext(ctx, "SELECT quiet_start,quiet_end,timezone FROM users WHERE id=?", actor).Scan(&start, &end, &zone); err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	status := start + "–" + end
	if start == end {
		status = "выключены"
	}
	r.send(ctx, b, actor, fmt.Sprintf("🌙 Тихие часы: %s\nТвой часовой пояс: %s\nВ это время уведомления ждут. На твои сообщения бот отвечает сразу.", status, zone), navButtons("23:00–09:00", "quiet_default", "00:00–10:00", "quiet_late", "✏️ Указать своё время", "quiet_custom", "🔔 Выключить тихие часы", "quiet_off", "🏠 Главное меню", "start"))
}
func (r *Router) quietMessage(ctx context.Context, b *bot.Bot, actor int64, text string) bool {
	if text == "/quiet" {
		r.showQuiet(ctx, b, actor)
		return true
	}
	if text == "/quiet_cancel" {
		_, err := r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_quiet'", actor)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
		} else {
			r.showQuiet(ctx, b, actor)
		}
		return true
	}
	if text == "/quiet_custom" {
		token, err := identity.RandomToken()
		if err == nil {
			_, err = r.Store.DB.ExecContext(ctx, "INSERT INTO drafts(id,user_id,kind,payload,created_at,expires_at) VALUES(?,?,'ui_quiet','{}',?,?)", token, actor, r.Store.Now().Unix(), r.Store.Now().Unix()+1800)
		}
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
		} else {
			r.send(ctx, b, actor, "Пришли начало и конец: например 22:30–08:30. Время — по твоему часовому поясу. Одинаковое начало и конец отключают тихие часы.", navButtons("Отмена", "quiet_cancel"))
		}
		return true
	}
	custom := false
	value := strings.TrimPrefix(text, "/quiet ")
	if !strings.HasPrefix(text, "/quiet ") {
		if strings.HasPrefix(text, "/") {
			return false
		}
		var count int
		if err := r.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM drafts WHERE user_id=? AND kind='ui_quiet' AND expires_at>?", actor, r.Store.Now().Unix()).Scan(&count); err != nil || count == 0 {
			return false
		}
		custom = true
		value = text
	}
	parts := strings.Split(strings.NewReplacer("–", "-", "—", "-").Replace(value), "-")
	if len(parts) != 2 {
		r.send(ctx, b, actor, "Укажи время, например 22:30–08:30.", navButtons("Отмена", "quiet_cancel"))
		return true
	}
	p := actions.QuietHours{Start: strings.TrimSpace(parts[0]), End: strings.TrimSpace(parts[1])}
	if err := p.Validate(); err != nil {
		r.send(ctx, b, actor, "Не получилось прочитать время. Пример: 22:30–08:30.", navButtons("Отмена", "quiet_cancel"))
		return true
	}
	if custom || strings.HasPrefix(text, "/quiet ") {
		if _, err := r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_quiet'", actor); err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
	}
	preview := fmt.Sprintf("Тихие часы будут %s–%s по твоему часовому поясу.", p.Start, p.End)
	if p.Start == p.End {
		preview = "Выключить тихие часы? Уведомления смогут приходить в любое время."
	}
	r.draft(ctx, b, actor, "quiet_hours", p, preview)
	return true
}
