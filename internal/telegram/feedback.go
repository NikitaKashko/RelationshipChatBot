package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/feedback"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (r *Router) showVisit(ctx context.Context, b *bot.Bot, actor, id int64) {
	facts := feedback.Service{Store: r.Store}
	if _, err := facts.VisitFor(ctx, r.Store.DB, actor, id, true); err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	rows, err := r.Store.DB.QueryContext(ctx, "SELECT p.id,p.title FROM visit_items vi JOIN places p ON p.id=vi.place_id WHERE vi.visit_id=? ORDER BY p.id", id)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	text := fmt.Sprintf(texts.T("internal.telegram.feedback.001"), id)
	type item struct {
		id    int64
		title string
	}
	var items []item
	for rows.Next() {
		var place int64
		var title string
		if err = rows.Scan(&place, &title); err != nil {
			break
		}
		text += fmt.Sprintf(texts.T("internal.telegram.feedback.002"), place, title, id, place)
		items = append(items, item{place, title})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	text += texts.T("internal.telegram.feedback.003")
	r.send(ctx, b, actor, text, nil)
	for _, p := range items {
		prefix := fmt.Sprintf("rate:%d:%d:", id, p.id)
		r.send(ctx, b, actor, texts.T("internal.telegram.feedback.004")+p.title+"»?", models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.feedback.005"), CallbackData: prefix + "liked"}, {Text: texts.T("internal.telegram.feedback.006"), CallbackData: prefix + "neutral"}, {Text: texts.T("internal.telegram.feedback.007"), CallbackData: prefix + "disliked"}}}})
	}
}
func (r *Router) feedbackCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	parts := strings.Split(q.Data, ":")
	if len(parts) < 1 || (parts[0] != "rate" && parts[0] != "again") {
		return false
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if (parts[0] == "rate" && len(parts) != 4) || (parts[0] == "again" && len(parts) != 5) {
		return true
	}
	visit, _ := strconv.ParseInt(parts[1], 10, 64)
	place, _ := strconv.ParseInt(parts[2], 10, 64)
	facts := feedback.Service{Store: r.Store}
	if _, err := facts.VisitFor(ctx, r.Store.DB, q.From.ID, visit, true); err != nil {
		r.send(ctx, b, q.From.ID, publicError(err), nil)
		return true
	}
	var count int
	if err := r.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM visit_items WHERE visit_id=? AND place_id=?", visit, place).Scan(&count); err != nil || count != 1 {
		r.send(ctx, b, q.From.ID, texts.T("internal.telegram.feedback.008"), nil)
		return true
	}
	rating := parts[3]
	if rating != "liked" && rating != "neutral" && rating != "disliked" {
		return true
	}
	if parts[0] == "rate" {
		prefix := fmt.Sprintf("again:%d:%d:%s:", visit, place, rating)
		r.send(ctx, b, q.From.ID, texts.T("internal.telegram.feedback.009"), models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.feedback.010"), CallbackData: prefix + "again"}, {Text: texts.T("internal.telegram.feedback.011"), CallbackData: prefix + "not_soon"}, {Text: texts.T("internal.telegram.feedback.012"), CallbackData: prefix + "never"}}}})
		return true
	}
	p := feedback.Review{Visit: visit, Place: place, Rating: rating, Again: parts[4]}
	// Editing with buttons preserves this author's previously written comment.
	if err := r.Store.DB.QueryRowContext(ctx, "SELECT coalesce((SELECT comment FROM reviews WHERE visit_id=? AND place_id=? AND user_id=?),'')", visit, place, q.From.ID).Scan(&p.Comment); err != nil {
		r.send(ctx, b, q.From.ID, publicError(err), nil)
		return true
	}
	if p.Again != "again" && p.Again != "not_soon" && p.Again != "never" {
		return true
	}
	preview := texts.T("internal.telegram.feedback.013") + map[string]string{"liked": texts.T("internal.telegram.feedback.005"), "neutral": texts.T("internal.telegram.feedback.006"), "disliked": texts.T("internal.telegram.feedback.007")}[p.Rating] + " · " + map[string]string{"again": texts.T("internal.telegram.feedback.010"), "not_soon": texts.T("internal.telegram.feedback.011"), "never": texts.T("internal.telegram.feedback.012")}[p.Again]
	if p.Again == "again" {
		preview += texts.T("internal.telegram.feedback.014")
	}
	r.draft(ctx, b, q.From.ID, "review", p, preview)
	return true
}
func (r *Router) feedbackMessage(ctx context.Context, b *bot.Bot, actor int64, cmd, args string) bool {
	parts := strings.Split(args, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	number := func(i int) int64 {
		if i >= len(parts) {
			return 0
		}
		n, _ := strconv.ParseInt(parts[i], 10, 64)
		return n
	}
	help := func(text string) bool {
		r.send(ctx, b, actor, text, nil)
		return true
	}
	switch cmd {
	case "/visit":
		if len(parts) != 4 && len(parts) != 5 {
			return help(texts.T("internal.telegram.feedback.015"))
		}
		if parts[3] != "freeze" && parts[3] != "nofreeze" {
			return help(texts.T("internal.telegram.feedback.016"))
		}
		p := feedback.Visit{Date: parts[0], Precision: parts[1], Freeze: parts[3] == "freeze"}
		for _, raw := range strings.Split(parts[2], ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || id < 1 {
				return help(texts.T("internal.telegram.feedback.017"))
			}
			p.Places = append(p.Places, id)
		}
		if len(parts) == 5 {
			p.Plan = number(4)
			if p.Plan < 1 {
				return help(texts.T("internal.telegram.feedback.018"))
			}
			plans := planning.Service{Store: r.Store}
			plan, err := plans.Get(ctx, r.Store.DB, actor, p.Plan)
			if err != nil {
				return help(publicError(err))
			}
			p.Version = plan.Version
			p.Freeze = true
		}
		preview := fmt.Sprintf(texts.T("internal.telegram.feedback.019"), p.Date, p.Precision, p.Places, p.Freeze)
		r.draft(ctx, b, actor, "visit_record", p, preview)
	case "/visitinfo":
		r.showVisit(ctx, b, actor, number(0))
	case "/correctvisit":
		r.draft(ctx, b, actor, "visit_correct", feedback.Correction{ID: number(0)}, texts.T("internal.telegram.feedback.020"))
	case "/notwent":
		plans := planning.Service{Store: r.Store}
		p, err := plans.Get(ctx, r.Store.DB, actor, number(0))
		if err != nil {
			return help(publicError(err))
		}
		r.draft(ctx, b, actor, "plan_not_went", planning.Cancel{ID: p.ID, Version: p.Version}, texts.T("internal.telegram.feedback.021")+p.Title+texts.T("internal.telegram.feedback.022"))
	case "/review":
		if len(parts) < 4 || len(parts) > 5 {
			return help(texts.T("internal.telegram.feedback.023"))
		}
		p := feedback.Review{Visit: number(0), Place: number(1), Rating: parts[2], Again: parts[3]}
		if len(parts) == 5 {
			p.Comment = parts[4]
		}
		r.draft(ctx, b, actor, "review", p, fmt.Sprintf(texts.T("internal.telegram.feedback.024"), p.Rating, p.Again, p.Comment))
	case "/freeze":
		if len(parts) != 2 {
			return help(texts.T("internal.telegram.feedback.025"))
		}
		p := feedback.Freeze{Place: number(0), Until: parts[1]}
		if p.Until == "off" {
			p.Until = ""
		}
		r.draft(ctx, b, actor, "freeze", p, texts.T("internal.telegram.feedback.026"))
	default:
		return false
	}
	return true
}
