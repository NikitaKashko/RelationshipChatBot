package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (r *Router) draft(ctx context.Context, b *bot.Bot, actor int64, kind string, p any, preview string) {
	raw, err := json.Marshal(p)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	svc := actions.Service{Store: r.Store}
	id, err := svc.Prepare(ctx, actor, kind, raw)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	label := texts.T("internal.telegram.ai.003")
	if plan, ok := p.(planning.Input); ok && plan.Draft {
		label = texts.T("internal.telegram.ai.005")
	}
	if kind == "consent" {
		if consent, ok := p.(map[string]any); ok && consent["kind"] == "ai" {
			if consent["enabled"] == true {
				label = texts.T("internal.telegram.collection.001")
			} else {
				label = texts.T("internal.telegram.ai.016")
			}
		}
	}
	r.send(ctx, b, actor, preview, button(label, "save:"+id))
}
func (r *Router) card(ctx context.Context, b *bot.Bot, actor, id int64) {
	r.cardWithNotice(ctx, b, actor, id, "")
}
func (r *Router) cardWithNotice(ctx context.Context, b *bot.Bot, actor, id int64, notice string) {
	cards := collection.Service{Store: r.Store}
	c, err := cards.Get(ctx, r.Store.DB, actor, id)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	key := strconv.FormatInt(id, 10)
	archiveText := texts.T("internal.telegram.collection.002")
	if c.Archived {
		archiveText = texts.T("internal.telegram.collection.003")
	}
	rows := placeCardRows(c, key, archiveText, notice != "")
	if len(c.Photos) > 0 {
		_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: actor, Photo: &models.InputFileString{Data: c.Photos[0]}, Caption: c.Title})
		if len(c.Photos) > 1 {
			rows = append(rows, []models.InlineKeyboardButton{{Text: texts.T("internal.telegram.collection.004"), CallbackData: "photos:" + key}})
		}
	}
	text := fmt.Sprintf("#%d · %s", id, collection.Render(c))
	if notice != "" {
		text = notice + "\n\n" + text
	}
	r.send(ctx, b, actor, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}
func placeCardRows(c collection.Card, key, archiveText string, justSaved bool) [][]models.InlineKeyboardButton {
	rows := [][]models.InlineKeyboardButton{}
	// A newly saved card already records the author as interested. The success
	// preview therefore shows the state in text instead of offering a no-op
	// interest button or an immediate archive action.
	if !justSaved {
		switch c.Mine {
		case "want":
			rows = append(rows, []models.InlineKeyboardButton{{Text: texts.T("internal.telegram.collection.005"), CallbackData: "uninterested:" + key}})
		case "uninterested":
			rows = append(rows, []models.InlineKeyboardButton{{Text: texts.T("internal.telegram.collection.006"), CallbackData: "want:" + key}})
		default:
			wantText := texts.T("internal.telegram.collection.007")
			if c.Theirs == "want" {
				wantText = texts.T("internal.telegram.collection.008")
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: wantText, CallbackData: "want:" + key}, {Text: texts.T("internal.telegram.collection.009"), CallbackData: "uninterested:" + key}})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: texts.T("internal.telegram.collection.010"), CallbackData: "candidate:" + key}})
	if !justSaved {
		rows = append(rows, []models.InlineKeyboardButton{{Text: archiveText, CallbackData: "archive:" + key}})
	}
	return rows
}
func (r *Router) collectionCallback(ctx context.Context, b *bot.Bot, q *models.CallbackQuery) bool {
	kind, raw, ok := strings.Cut(q.Data, ":")
	if !ok {
		return false
	}
	if kind != "want" && kind != "uninterested" && kind != "candidate" && kind != "archive" && kind != "photos" {
		return false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return true
	}
	cards := collection.Service{Store: r.Store}
	c, err := cards.Get(ctx, r.Store.DB, q.From.ID, id)
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if err != nil {
		r.send(ctx, b, q.From.ID, publicError(err), nil)
		return true
	}
	if kind == "photos" {
		if len(c.Photos) > 1 {
			for _, photo := range c.Photos[1:] {
				_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: q.From.ID, Photo: &models.InputFileString{Data: photo}})
			}
		}
		return true
	}
	if kind == "archive" {
		r.draft(ctx, b, q.From.ID, "place_archive", collection.Archive{ID: id, Version: c.Version, Archived: !c.Archived}, texts.T("internal.telegram.collection.011")+c.Title+"»?")
		return true
	}
	if kind == "candidate" {
		u, _ := store.UserFor(ctx, r.Store.DB, q.From.ID, false)
		r.draft(ctx, b, q.From.ID, "plan_save", planning.Input{Draft: true, Title: c.Title, Timezone: u.Timezone, Places: []int64{id}}, texts.T("internal.telegram.collection.012")+c.Title+texts.T("internal.telegram.collection.013"))
		return true
	}
	interest := "want"
	if kind == "uninterested" {
		interest = "uninterested"
	}
	// The explicit preference button is the confirmation itself; no model writes.
	p, _ := json.Marshal(collection.Interest{ID: id, Interest: interest})
	svc := actions.Service{Store: r.Store}
	draft, err := svc.Prepare(ctx, q.From.ID, "place_interest", p)
	if err == nil {
		_, err = svc.Apply(ctx, q.From.ID, draft)
	}
	if err != nil {
		r.send(ctx, b, q.From.ID, publicError(err), nil)
	} else {
		r.card(ctx, b, q.From.ID, id)
	}
	return true
}
func (r *Router) saved(ctx context.Context, b *bot.Bot, actor int64, result json.RawMessage) {
	var out struct {
		QuietSaved      bool  `json:"quiet_saved"`
		CollectionAdded bool  `json:"collection_added"`
		Place           int64 `json:"place"`
		Plan            int64 `json:"plan"`
		Visit           int64 `json:"visit"`
	}
	_ = json.Unmarshal(result, &out)
	if out.QuietSaved {
		r.showQuiet(ctx, b, actor)
		return
	}
	if out.Visit != 0 {
		r.showVisit(ctx, b, actor, out.Visit)
		return
	}
	if out.Place != 0 {
		if out.CollectionAdded {
			r.cardWithNotice(ctx, b, actor, out.Place, texts.T("internal.telegram.collection.014"))
			return
		}
		r.card(ctx, b, actor, out.Place)
		return
	}
	if out.Plan != 0 {
		svc := planning.Service{Store: r.Store}
		p, err := svc.Get(ctx, r.Store.DB, actor, out.Plan)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		status := map[string]string{"planned": texts.T("internal.telegram.collection.015"), "cancelled": texts.T("internal.telegram.collection.016"), "visited": texts.T("internal.telegram.collection.017"), "not_went": texts.T("internal.telegram.collection.018")}[p.Status]
		text := fmt.Sprintf(texts.T("internal.telegram.collection.019"), p.ID, p.Title, p.Start.In(loc).Format("02.01.2006 15:04"), p.End.In(loc).Format("02.01.2006 15:04"), status)
		if p.Draft {
			text = fmt.Sprintf(texts.T("internal.telegram.collection.020"), p.ID, p.Title)
			if p.Start.Unix() != 0 {
				text += texts.T("internal.planning.plans.011") + p.Start.In(loc).Format("02.01.2006")
			}
		} else if p.DateOnly {
			text = fmt.Sprintf(texts.T("internal.telegram.collection.021"), p.ID, p.Title, p.Start.In(loc).Format("02.01.2006"), status)
		}
		if !p.Draft && !p.DateOnly && !p.HasEnd {
			text = fmt.Sprintf(texts.T("internal.telegram.collection.022"), p.ID, p.Title, p.Start.In(loc).Format("02.01.2006 15:04"), status)
		}
		if p.Description != "" {
			text += "\n" + p.Description
		}
		for i, c := range p.Items {
			text += fmt.Sprintf("\n%d. %s", i+1, c.Title)
		}
		r.send(ctx, b, actor, text, r.planButtons(p))
		return
	}
	r.home(ctx, b, actor)
}
func (r *Router) collectionMessage(ctx context.Context, b *bot.Bot, m *models.Message) bool {
	actor := m.From.ID
	text := strings.TrimSpace(m.Text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		fields = strings.Fields(m.Caption)
	}
	if len(fields) == 0 {
		return false
	}
	command := strings.Split(fields[0], "@")[0]
	args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if r.affectionMessage(ctx, b, actor, command, args) {
		return true
	}
	if r.feedbackMessage(ctx, b, actor, command, args) {
		return true
	}
	cards := collection.Service{Store: r.Store}
	switch command {
	case "/plans", "/candidates", "/drafts":
		pair, err := store.PairFor(ctx, r.Store.DB, actor)
		if err != nil || pair == 0 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.023"), nil)
			return true
		}
		query := "SELECT id FROM plans WHERE pair_id=? AND is_draft=0 ORDER BY start_at DESC"
		if command == "/candidates" || command == "/drafts" {
			query = "SELECT id FROM plans WHERE pair_id=? AND is_draft=1 AND status='planned' ORDER BY id DESC"
		}
		rows, err := r.Store.DB.QueryContext(ctx, query, pair)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		r.send(ctx, b, actor, texts.T("internal.telegram.collection.024"), r.dateButtons(ctx, actor))
		page := 0
		if len(fields) > 1 {
			page, _ = strconv.Atoi(fields[1])
		}
		if page < 0 {
			page = 0
		}
		if page > len(ids)/5 || page*5 >= len(ids) {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.025"), nil)
			return true
		}
		end := page*5 + 5
		if end > len(ids) {
			end = len(ids)
		}
		for _, id := range ids[page*5 : end] {
			{
				raw, _ := json.Marshal(map[string]int64{"plan": id})
				r.saved(ctx, b, actor, raw)
			}
		}
		if end < len(ids) {
			r.send(ctx, b, actor, fmt.Sprintf(texts.T("internal.telegram.collection.026"), command, page+1), nil)
		}
		return true
	case "/uncandidate":
		r.send(ctx, b, actor, texts.T("internal.telegram.collection.027"), r.dateButtons(ctx, actor))
		return true
	case "/location":
		parts := strings.Split(args, "|")
		if len(parts) != 2 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.028"), nil)
			return true
		}
		p := identity.Location{City: strings.TrimSpace(parts[0]), Timezone: strings.TrimSpace(parts[1])}
		r.draft(ctx, b, actor, "location_proposal", p, texts.T("internal.telegram.collection.029")+p.City+texts.T("internal.telegram.collection.030")+p.Timezone)
		return true
	case "/add":
		if args == "" {
			r.addOptions(ctx, b, actor)
			return true
		}
		// Keep the explicit pipe form as the manual alternative; a link or free
		// description goes through AI identification and a reviewed search card.
		enabled, _, _ := r.aiStatus(ctx, actor)
		if enabled && !strings.Contains(args, "|") && r.AI != nil && r.AI.Provider != nil {
			r.aiMessage(ctx, b, actor, texts.T("internal.telegram.collection.031")+args)
			return true
		}
		p := collection.Input{Title: args, Categories: []string{texts.T("internal.ai.search_provider.002")}}
		parts := strings.Split(args, "|")
		p.Title = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			p.Categories = strings.Split(parts[1], ",")
		}
		if len(parts) > 2 {
			p.Address = strings.TrimSpace(parts[2])
		}
		if len(parts) > 3 {
			p.MapURL = strings.TrimSpace(parts[3])
		}
		if strings.HasPrefix(args, "https://") && len(parts) == 1 {
			p.Title = texts.T("internal.telegram.collection.032")
			p.MapURL = args
			resolved, err := collection.ResolveMap(ctx, args)
			if err == nil {
				p.MapURL = resolved
			}
			p.Description = texts.T("internal.telegram.collection.033")
		}
		r.draft(ctx, b, actor, "place_create", p, collection.Render(collection.Card{Input: p, Author: m.From.FirstName}))
		return true
	case "/places":
		filter := "all"
		page := 0
		if len(fields) > 1 {
			filter = fields[1]
		}
		if len(fields) > 2 {
			page, _ = strconv.Atoi(fields[2])
		}
		if page < 0 {
			page = 0
		}
		list, err := cards.List(ctx, actor, filter)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		if page > len(list)/5 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.034"), navButtons(texts.T("internal.telegram.collection.035"), "add"))
			return true
		}
		start := page * 5
		if start >= len(list) {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.034"), navButtons(texts.T("internal.telegram.collection.035"), "add"))
			return true
		}
		end := start + 5
		if end > len(list) {
			end = len(list)
		}
		r.send(ctx, b, actor, texts.T("internal.telegram.collection.036"), navButtons(texts.T("internal.telegram.collection.035"), "add"))
		for _, c := range list[start:end] {
			r.card(ctx, b, actor, c.ID)
		}
		if end < len(list) {
			r.send(ctx, b, actor, fmt.Sprintf(texts.T("internal.telegram.collection.037"), filter, page+1), nil)
		}
		return true
	case "/edit":
		parts := strings.Split(args, "|")
		id, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil || len(parts) < 2 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.038"), nil)
			return true
		}
		c, err := cards.Get(ctx, r.Store.DB, actor, id)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		p := c.Input
		p.Title = strings.TrimSpace(parts[1])
		if len(parts) > 2 {
			p.Categories = strings.Split(parts[2], ",")
		}
		if len(parts) > 3 {
			p.Address = strings.TrimSpace(parts[3])
		}
		if len(parts) > 4 {
			p.MapURL = strings.TrimSpace(parts[4])
		}
		r.draft(ctx, b, actor, "place_edit", collection.Edit{ID: id, Version: c.Version, Card: p}, collection.Render(collection.Card{Input: p, Author: c.Author}))
		return true
	case "/photo":
		if len(fields) != 2 || len(m.Photo) == 0 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.039"), nil)
			return true
		}
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return true
		}
		c, err := cards.Get(ctx, r.Store.DB, actor, id)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		if len(c.Photos) >= 3 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.040"), nil)
			return true
		}
		p := c.Input
		p.Photos = append(p.Photos, m.Photo[len(m.Photo)-1].FileID)
		r.draft(ctx, b, actor, "place_edit", collection.Edit{ID: id, Version: c.Version, Card: p}, texts.T("internal.telegram.collection.041")+c.Title+"»?")
		return true
	case "/plan":
		parts := strings.Split(args, "|")
		if len(parts) != 5 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.042"), nil)
			return true
		}
		p := planning.Input{Title: strings.TrimSpace(parts[0]), Start: strings.TrimSpace(parts[1]), End: strings.TrimSpace(parts[2]), Timezone: strings.TrimSpace(parts[3])}
		for _, v := range strings.Split(parts[4], ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				r.send(ctx, b, actor, texts.T("internal.telegram.collection.043"), nil)
				return true
			}
			p.Places = append(p.Places, id)
		}
		svc := planning.Service{Store: r.Store}
		preview, err := svc.Preview(ctx, r.Store.DB, actor, p)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		r.draft(ctx, b, actor, "plan_save", p, preview)
		return true
	case "/reschedule", "/cancelplan":
		parts := strings.Split(args, "|")
		id, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.044"), nil)
			return true
		}
		svc := planning.Service{Store: r.Store}
		old, err := svc.Get(ctx, r.Store.DB, actor, id)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		if command == "/cancelplan" {
			r.draft(ctx, b, actor, "plan_cancel", planning.Cancel{ID: id, Version: old.Version}, texts.T("internal.telegram.collection.045")+old.Title+texts.T("internal.telegram.collection.046"))
			return true
		}
		if len(parts) != 3 {
			r.send(ctx, b, actor, texts.T("internal.telegram.collection.047"), nil)
			return true
		}
		p := planning.Input{ID: id, Version: old.Version, Title: old.Title, Timezone: old.Timezone, Start: strings.TrimSpace(parts[1]), End: strings.TrimSpace(parts[2])}
		for _, c := range old.Items {
			p.Places = append(p.Places, c.ID)
		}
		preview, err := svc.Preview(ctx, r.Store.DB, actor, p)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		r.draft(ctx, b, actor, "plan_save", p, preview)
		return true
	}
	return false
}
func (r *Router) duplicate(ctx context.Context, b *bot.Bot, actor int64, err error) bool {
	var d *collection.Duplicate
	if !errors.As(err, &d) {
		return false
	}
	r.send(ctx, b, actor, texts.T("internal.telegram.collection.048"), nil)
	r.card(ctx, b, actor, d.ID)
	return true
}
