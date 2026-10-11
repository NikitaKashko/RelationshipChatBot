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
	r.send(ctx, b, actor, preview, button("Сохранить", "save:"+id))
}

func (r *Router) card(ctx context.Context, b *bot.Bot, actor, id int64) {
	cards := collection.Service{Store: r.Store}
	c, err := cards.Get(ctx, r.Store.DB, actor, id)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	key := strconv.FormatInt(id, 10)
	archiveText := "В архив"
	if c.Archived {
		archiveText = "Вернуть из архива"
	}
	rows := [][]models.InlineKeyboardButton{{{Text: "Хочу сходить", CallbackData: "want:" + key}, {Text: "Не интересно", CallbackData: "uninterested:" + key}}, {{Text: "В кандидаты", CallbackData: "candidate:" + key}, {Text: archiveText, CallbackData: "archive:" + key}}}
	if len(c.Photos) > 0 {
		_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: actor, Photo: &models.InputFileString{Data: c.Photos[0]}, Caption: c.Title})
		if len(c.Photos) > 1 {
			rows = append(rows, []models.InlineKeyboardButton{{Text: "Ещё фото", CallbackData: "photos:" + key}})
		}
	}
	r.send(ctx, b, actor, fmt.Sprintf("#%d · %s", id, collection.Render(c)), models.InlineKeyboardMarkup{InlineKeyboard: rows})
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
		r.draft(ctx, b, q.From.ID, "place_archive", collection.Archive{ID: id, Version: c.Version, Archived: !c.Archived}, "Изменить архивный статус «"+c.Title+"»?")
		return true
	}
	if kind == "candidate" {
		r.draft(ctx, b, q.From.ID, "candidate", planning.Candidate{ID: id}, "Добавить «"+c.Title+"» в кандидаты ближайшего свидания? Это ещё не согласованный план.")
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
		Place int64 `json:"place"`
		Plan  int64 `json:"plan"`
		Visit int64 `json:"visit"`
	}
	_ = json.Unmarshal(result, &out)
	if out.Visit != 0 {
		r.showVisit(ctx, b, actor, out.Visit)
		return
	}
	if out.Place != 0 {
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
		status := map[string]string{"planned": "Запланировано", "cancelled": "Отменено", "visited": "Посетили", "not_went": "Не пошли"}[p.Status]
		text := fmt.Sprintf("План #%d · %s\n%s — %s\nСтатус: %s", p.ID, p.Title, p.Start.In(loc).Format("02.01.2006 15:04"), p.End.In(loc).Format("02.01.2006 15:04"), status)
		for i, c := range p.Items {
			text += fmt.Sprintf("\n%d. %s", i+1, c.Title)
		}
		r.send(ctx, b, actor, text, nil)
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
	case "/plans", "/candidates":
		pair, err := store.PairFor(ctx, r.Store.DB, actor)
		if err != nil || pair == 0 {
			r.send(ctx, b, actor, "Этот список доступен после создания пары.", nil)
			return true
		}
		query := "SELECT id FROM plans WHERE pair_id=? ORDER BY start_at DESC"
		if command == "/candidates" {
			query = "SELECT place_id FROM candidates WHERE pair_id=? ORDER BY position"
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
		page := 0
		if len(fields) > 1 {
			page, _ = strconv.Atoi(fields[1])
		}
		if page < 0 {
			page = 0
		}
		if page > len(ids)/5 || page*5 >= len(ids) {
			r.send(ctx, b, actor, "Список пока пуст.", nil)
			return true
		}
		end := page*5 + 5
		if end > len(ids) {
			end = len(ids)
		}
		for _, id := range ids[page*5 : end] {
			if command == "/candidates" {
				r.card(ctx, b, actor, id)
			} else {
				raw, _ := json.Marshal(map[string]int64{"plan": id})
				r.saved(ctx, b, actor, raw)
			}
		}
		if end < len(ids) {
			r.send(ctx, b, actor, fmt.Sprintf("Следующая страница: %s %d", command, page+1), nil)
		}
		return true
	case "/uncandidate":
		id, err := strconv.ParseInt(args, 10, 64)
		if err != nil {
			r.send(ctx, b, actor, "Укажи /uncandidate номер_места.", nil)
			return true
		}
		c, err := cards.Get(ctx, r.Store.DB, actor, id)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		r.draft(ctx, b, actor, "candidate", planning.Candidate{ID: id, Remove: true}, "Убрать «"+c.Title+"» из кандидатов? В копилке место останется.")
		return true
	case "/location":
		parts := strings.Split(args, "|")
		if len(parts) != 2 {
			r.send(ctx, b, actor, "Укажи /location город | Europe/Moscow. Второй партнёр подтвердит предложение через /start.", nil)
			return true
		}
		p := identity.Location{City: strings.TrimSpace(parts[0]), Timezone: strings.TrimSpace(parts[1])}
		r.draft(ctx, b, actor, "location_proposal", p, "Предложить город пары: "+p.City+"\nЧасовой пояс: "+p.Timezone)
		return true
	case "/add":
		if args == "" {
			r.send(ctx, b, actor, "Пришли /add название. Можно подробнее: /add название | категории через запятую | адрес | ссылка на Яндекс Карты\nСтартовые категории: "+strings.Join(collection.Starters, ", ")+". Можно указать свои.", nil)
			return true
		}
		p := collection.Input{Title: args, Categories: []string{"Другое"}}
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
			p.Title = "Идея по ссылке"
			p.MapURL = args
			resolved, err := collection.ResolveMap(ctx, args)
			if err == nil {
				p.MapURL = resolved
			}
			p.Description = "Данные места не проверены. Название можно уточнить перед использованием."
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
			r.send(ctx, b, actor, "В этом списке пока нет мест.", nil)
			return true
		}
		start := page * 5
		if start >= len(list) {
			r.send(ctx, b, actor, "В этом списке пока нет мест.", nil)
			return true
		}
		end := start + 5
		if end > len(list) {
			end = len(list)
		}
		for _, c := range list[start:end] {
			r.card(ctx, b, actor, c.ID)
		}
		if end < len(list) {
			r.send(ctx, b, actor, fmt.Sprintf("Следующая страница: /places %s %d", filter, page+1), nil)
		}
		return true
	case "/edit":
		parts := strings.Split(args, "|")
		id, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil || len(parts) < 2 {
			r.send(ctx, b, actor, "Правка: /edit номер | название | категории | адрес | карта. Пропущенные поля сохраняются.", nil)
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
			r.send(ctx, b, actor, "Пришли фотографию с подписью /photo номер_места.", nil)
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
			r.send(ctx, b, actor, "У карточки уже три фото.", nil)
			return true
		}
		p := c.Input
		p.Photos = append(p.Photos, m.Photo[len(m.Photo)-1].FileID)
		r.draft(ctx, b, actor, "place_edit", collection.Edit{ID: id, Version: c.Version, Card: p}, "Добавить твоё фото к «"+c.Title+"»?")
		return true
	case "/plan":
		parts := strings.Split(args, "|")
		if len(parts) != 5 {
			r.send(ctx, b, actor, "Ручной план: /plan название | начало YYYY-MM-DD HH:MM | окончание YYYY-MM-DD HH:MM | Europe/Moscow | номера мест через запятую. ИИ-интерфейс подключим отдельно.", nil)
			return true
		}
		p := planning.Input{Title: strings.TrimSpace(parts[0]), Start: strings.TrimSpace(parts[1]), End: strings.TrimSpace(parts[2]), Timezone: strings.TrimSpace(parts[3])}
		for _, v := range strings.Split(parts[4], ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				r.send(ctx, b, actor, "Проверь номера мест.", nil)
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
			r.send(ctx, b, actor, "Укажи номер плана.", nil)
			return true
		}
		svc := planning.Service{Store: r.Store}
		old, err := svc.Get(ctx, r.Store.DB, actor, id)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return true
		}
		if command == "/cancelplan" {
			r.draft(ctx, b, actor, "plan_cancel", planning.Cancel{ID: id, Version: old.Version}, "Отменить «"+old.Title+"»? Посещение и фриз не будут добавлены.")
			return true
		}
		if len(parts) != 3 {
			r.send(ctx, b, actor, "Перенос: /reschedule номер | новое начало YYYY-MM-DD HH:MM | новое окончание YYYY-MM-DD HH:MM", nil)
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
	r.send(ctx, b, actor, "Это место уже есть. Автор сохранён — можешь нажать «Хочу сходить» на существующей карточке.", nil)
	r.card(ctx, b, actor, d.ID)
	return true
}
