package telegram

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/affection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type entry struct {
	Kind     string
	Step     int
	Card     collection.Input
	Plan     planning.Input
	Location identity.Location
}

func (r *Router) addOptions(ctx context.Context, b *bot.Bot, actor int64) {
	enabled, _, err := r.aiStatus(ctx, actor)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	if !enabled {
		r.startEntry(ctx, b, actor, entry{Kind: "card"})
		return
	}
	r.send(ctx, b, actor, texts.T("internal.telegram.entry.001"), navButtons(texts.T("internal.telegram.entry.002"), "add_ai", texts.T("internal.telegram.entry.003"), "add_manual"))
}
func (r *Router) dateButtons(ctx context.Context, actor int64) models.InlineKeyboardMarkup {
	items := []string{texts.T("internal.telegram.entry.004"), "drafts", texts.T("internal.telegram.entry.005"), "draft_new", texts.T("internal.telegram.entry.006"), "plan_new"}
	if _, err := store.AIFor(ctx, r.Store.DB, actor); err == nil {
		items = append(items, texts.T("internal.telegram.entry.007"), "date_ai")
	}
	return navButtons(items...)
}
func (r *Router) planButtons(p planning.Plan) models.InlineKeyboardMarkup {
	if p.Status != "planned" {
		return navButtons(texts.T("internal.telegram.entry.008"), "drafts")
	}
	id := strconv.FormatInt(p.ID, 10)
	rows := [][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.entry.009"), CallbackData: "entry:edit:" + id}}, {{Text: texts.T("internal.telegram.entry.010"), CallbackData: "entry:cancel:" + id}}}
	if p.Draft {
		rows = append([][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.entry.011"), CallbackData: "entry:date:" + id}}}, rows...)
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
func (r *Router) startEntry(ctx context.Context, b *bot.Bot, actor int64, e entry) {
	u, err := store.UserFor(ctx, r.Store.DB, actor, false)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	if e.Kind != "card" && e.Kind != "lookup" {
		pair, err := store.PairFor(ctx, r.Store.DB, actor)
		if err != nil || pair == 0 {
			r.send(ctx, b, actor, texts.T("internal.telegram.entry.012"), nil)
			return
		}
		if (e.Kind == "plan" || e.Kind == "date") && e.Plan.Timezone == "" {
			var zone *string
			if err = r.Store.DB.QueryRowContext(ctx, "SELECT timezone FROM pairs WHERE id=?", pair).Scan(&zone); err != nil {
				r.send(ctx, b, actor, publicError(err), nil)
				return
			}
			e.Plan.Timezone = u.Timezone
			if zone != nil {
				e.Plan.Timezone = *zone
			}
		}
	}
	if e.Kind == "card" {
		e.Card.City = u.City
	}
	r.saveEntry(ctx, b, actor, e)
}
func (r *Router) saveEntry(ctx context.Context, b *bot.Bot, actor int64, e entry) {
	raw, _ := json.Marshal(e)
	token, err := identity.RandomToken()
	if err == nil {
		err = r.Store.Write(ctx, func(q store.Query) error {
			if _, err := q.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_entry'", actor); err != nil {
				return err
			}
			_, err := q.ExecContext(ctx, "INSERT INTO drafts(id,user_id,kind,payload,created_at,expires_at) VALUES(?,?,'ui_entry',?,?,?)", token, actor, string(raw), r.Store.Now().Unix(), r.Store.Now().Unix()+1800)
			return err
		})
	}
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	prompt := ""
	switch e.Kind {
	case "nickname":
		prompt = texts.T("internal.telegram.entry.013")
	case "lookup":
		prompt = texts.T("internal.telegram.entry.014")
	case "card":
		prompt = []string{texts.T("internal.telegram.entry.015"), texts.T("internal.telegram.entry.016"), texts.T("internal.telegram.entry.017"), texts.T("internal.telegram.entry.018")}[e.Step]
	case "date":
		prompt = texts.T("internal.telegram.entry.019")
	case "pair_location":
		prompt = []string{texts.T("internal.telegram.entry.032"), texts.T("internal.telegram.entry.033")}[e.Step]
	default:
		prompt = []string{texts.T("internal.telegram.entry.020"), texts.T("internal.telegram.entry.021"), texts.T("internal.telegram.entry.022"), texts.T("internal.telegram.entry.023")}[e.Step]
	}
	markup := navButtons(texts.T("internal.telegram.entry.024"), "entry_cancel")
	if e.Kind == "pair_location" && e.Step == 1 {
		markup = models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.router.007"), CallbackData: "entry:location_moscow"}}, {{Text: texts.T("internal.telegram.entry.024"), CallbackData: "menu:entry_cancel"}}}}
	}
	if (e.Kind == "card" && e.Step >= 2) || (e.Kind == "plan" && (e.Step == 1 || e.Step == 2)) {
		markup = navButtons(texts.T("internal.telegram.entry.025"), "skip", texts.T("internal.telegram.entry.024"), "entry_cancel")
	}
	r.send(ctx, b, actor, prompt, markup)
}
func (r *Router) entryCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	if !strings.HasPrefix(q.Data, "entry:") {
		return false
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if q.Data == "entry:location_moscow" {
		r.entryMessage(ctx, b, q.From.ID, "Europe/Moscow")
		return true
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 {
		return true
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return true
	}
	svc := planning.Service{Store: r.Store}
	p, err := svc.Get(ctx, r.Store.DB, q.From.ID, id)
	if err != nil {
		r.send(ctx, b, q.From.ID, publicError(err), nil)
		return true
	}
	input := planning.Input{ID: p.ID, Version: p.Version, Title: p.Title, Description: p.Description, Draft: p.Draft, Timezone: p.Timezone}
	for _, c := range p.Items {
		input.Places = append(input.Places, c.ID)
	}
	if !p.Draft || p.Start.Unix() != 0 {
		if p.DateOnly {
			loc, _ := time.LoadLocation(p.Timezone)
			input.Date = p.Start.In(loc).Format("2006-01-02")
		} else {
			input.Start = p.Start.Format("2006-01-02T15:04:05Z07:00")
			if p.HasEnd {
				input.End = p.End.Format("2006-01-02T15:04:05Z07:00")
			}
		}
	}
	switch parts[1] {
	case "cancel":
		r.draft(ctx, b, q.From.ID, "plan_cancel", planning.Cancel{ID: p.ID, Version: p.Version}, texts.T("internal.telegram.collection.045")+p.Title+"»?")
	case "date":
		input.Draft = false
		r.startEntry(ctx, b, q.From.ID, entry{Kind: "date", Plan: input})
	case "edit":
		r.startEntry(ctx, b, q.From.ID, entry{Kind: "plan", Plan: input})
	}
	return true
}
func (r *Router) entryMessage(ctx context.Context, b *bot.Bot, actor int64, text string) bool {
	switch text {
	case "/names_edit":
		r.startEntry(ctx, b, actor, entry{Kind: "nickname"})
		return true
	case "/entry_cancel":
		r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_entry'", actor)
		r.home(ctx, b, actor)
		return true
	case "/add_manual":
		r.startEntry(ctx, b, actor, entry{Kind: "card"})
		return true
	case "/add_ai":
		enabled, _, err := r.aiStatus(ctx, actor)
		if err != nil || !enabled {
			r.addOptions(ctx, b, actor)
			return true
		}
		r.startEntry(ctx, b, actor, entry{Kind: "lookup"})
		return true
	case "/draft_new":
		r.startEntry(ctx, b, actor, entry{Kind: "plan", Plan: planning.Input{Draft: true}})
		return true
	case "/plan_new":
		r.startEntry(ctx, b, actor, entry{Kind: "plan"})
		return true
	case "/date_ai":
		if _, err := store.AIFor(ctx, r.Store.DB, actor); err != nil {
			r.send(ctx, b, actor, texts.T("internal.telegram.entry.026"), r.dateButtons(ctx, actor))
			return true
		}
		r.aiMessage(ctx, b, actor, texts.T("internal.telegram.entry.027"))
		return true
	}
	if strings.HasPrefix(text, "/") && text != "/skip" {
		r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_entry'", actor)
		return false
	}
	var raw string
	if err := r.Store.DB.QueryRowContext(ctx, "SELECT payload FROM drafts WHERE user_id=? AND kind='ui_entry' AND expires_at>? ORDER BY created_at DESC LIMIT 1", actor, r.Store.Now().Unix()).Scan(&raw); err != nil {
		return false
	}
	var e entry
	if json.Unmarshal([]byte(raw), &e) != nil {
		return false
	}
	value := text
	if text == "/skip" {
		value = ""
	}
	done := false
	switch e.Kind {
	case "nickname":
		if value == "" || len([]rune(value)) > 60 || strings.ContainsAny(value, "\r\n") {
			r.send(ctx, b, actor, texts.T("internal.telegram.entry.028"), nil)
			return true
		}
		r.draft(ctx, b, actor, "notification_names", affection.Names{Partner: value}, texts.T("internal.telegram.affection.015")+value+texts.T("internal.telegram.affection.016"))
		r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_entry'", actor)
		return true
	case "lookup":
		r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_entry'", actor)
		r.aiMessage(ctx, b, actor, texts.T("internal.telegram.collection.031")+text)
		return true
	case "card":
		switch e.Step {
		case 0:
			if value == "" {
				r.send(ctx, b, actor, texts.T("internal.telegram.entry.029"), nil)
				return true
			}
			e.Card.Title = value
		case 1:
			if value == "" {
				value = texts.T("internal.ai.search_provider.002")
			}
			e.Card.Categories = strings.Split(value, ",")
		case 2:
			e.Card.Address = value
		case 3:
			e.Card.MapURL = value
			done = true
		}
		e.Step++
	case "date":
		e.Plan.Date = value
		e.Plan.Start = ""
		e.Plan.End = ""
		done = true
	case "pair_location":
		switch e.Step {
		case 0:
			if value == "" || len([]rune(value)) > 100 || strings.ContainsAny(value, "\r\n") {
				r.send(ctx, b, actor, texts.T("internal.telegram.entry.034"), nil)
				return true
			}
			e.Location.City = value
		case 1:
			zone, err := normalizeTimezone(value)
			if err != nil {
				r.send(ctx, b, actor, texts.T("internal.telegram.entry.035"), nil)
				return true
			}
			e.Location.Timezone = zone
			done = true
		}
		e.Step++
	case "plan":
		switch e.Step {
		case 0:
			if value == "" {
				r.send(ctx, b, actor, texts.T("internal.telegram.entry.029"), nil)
				return true
			}
			e.Plan.Title = value
		case 1:
			e.Plan.Description = value
		case 2:
			e.Plan.Places = nil
			if value != "" {
				for _, v := range strings.Split(value, ",") {
					id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
					if err != nil {
						r.send(ctx, b, actor, texts.T("internal.telegram.entry.030"), nil)
						return true
					}
					e.Plan.Places = append(e.Plan.Places, id)
				}
			}
			done = e.Plan.Draft
		case 3:
			e.Plan.Date = value
			e.Plan.Start = ""
			e.Plan.End = ""
			done = true
		}
		e.Step++
	}
	if done {
		if e.Kind == "pair_location" {
			r.draft(ctx, b, actor, "location_proposal", e.Location, texts.T("internal.telegram.collection.029")+e.Location.City+texts.T("internal.telegram.collection.030")+e.Location.Timezone)
		} else if e.Kind == "card" {
			if err := collection.Validate(e.Card); err != nil {
				e.Step = 3
				r.saveEntry(ctx, b, actor, e)
				r.send(ctx, b, actor, publicError(err), nil)
				return true
			}
			r.draft(ctx, b, actor, "place_create", e.Card, collection.Render(collection.Card{Input: e.Card, Author: texts.T("internal.telegram.entry.031")}))
		} else {
			svc := planning.Service{Store: r.Store}
			preview, err := svc.Preview(ctx, r.Store.DB, actor, e.Plan)
			if err != nil {
				if e.Kind == "plan" {
					if e.Plan.Draft {
						e.Step = 2
					} else {
						e.Step = 3
					}
					r.saveEntry(ctx, b, actor, e)
				}
				r.send(ctx, b, actor, publicError(err), nil)
				return true
			}
			r.draft(ctx, b, actor, "plan_save", e.Plan, preview)
		}
		r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_entry'", actor)
	} else {
		r.saveEntry(ctx, b, actor, e)
	}
	return true
}
