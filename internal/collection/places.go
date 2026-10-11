// Package collection owns scoped cards; writes run inside the action transaction.
package collection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

var Starters = []string{"Погулять", "Покушать", "Музыка", "Кино", "Поездка", "Другое"}

type Source struct {
	URL         string `json:"url"`
	RetrievedAt int64  `json:"retrieved_at"`
}
type Input struct {
	Title       string   `json:"title"`
	Categories  []string `json:"categories"`
	Address     string   `json:"address,omitempty"`
	City        string   `json:"city,omitempty"`
	MapURL      string   `json:"map_url,omitempty"`
	Description string   `json:"description,omitempty"`
	Price       *float64 `json:"price,omitempty"`
	PriceBasis  string   `json:"price_basis,omitempty"`
	Sources     []Source `json:"sources,omitempty"`
	Photos      []string `json:"photos,omitempty"`
}

// SearchSave atomically saves a reviewed search card and, optionally, a candidate.
type SearchSave struct {
	Card      Input `json:"card"`
	Candidate bool  `json:"candidate"`
}
type Card struct {
	Input
	ID           int64  `json:"id"`
	AuthorID     *int64 `json:"author_id"`
	Author       string `json:"author"`
	Version      int64  `json:"version"`
	Archived     bool   `json:"archived"`
	Mine, Theirs string
	Refused      bool
	FreezeUntil  float64
	Visited      bool
}
type Duplicate struct{ ID int64 }

func (d *Duplicate) Error() string { return "место уже есть в коллекции" }

type Service struct{ Store *store.Store }

// URLs are references, not permission to fetch them. Network resolution uses
// the separate SSRF-protected resolver, never this storage transaction.
func ValidURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || !strings.Contains(h, ".") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	return true
}

func Validate(p Input) error {
	if strings.TrimSpace(p.Title) == "" || len([]rune(p.Title)) > 200 {
		return errors.New("укажи название до 200 символов")
	}
	if len(p.Categories) == 0 || len(p.Categories) > 20 {
		return errors.New("укажи хотя бы одну категорию")
	}
	for _, c := range p.Categories {
		if strings.TrimSpace(c) == "" || len([]rune(c)) > 60 {
			return errors.New("неверная категория")
		}
	}
	if p.MapURL != "" && !ValidURL(p.MapURL) {
		return errors.New("неверная ссылка на карту")
	}
	if p.Price != nil && (math.IsNaN(*p.Price) || math.IsInf(*p.Price, 0) || *p.Price < 0 || (p.PriceBasis != "person" && p.PriceBasis != "couple")) {
		return errors.New("цена требует значения и указания на человека или на двоих")
	}
	if p.Price == nil && p.PriceBasis != "" {
		return errors.New("не указана цена")
	}
	if len(p.Photos) > 3 {
		return errors.New("доступно до трёх фотографий")
	}
	for _, id := range p.Photos {
		if id == "" || strings.ContainsAny(id, "/:\n\r ") {
			return errors.New("фото должно быть Telegram file_id")
		}
	}
	for _, src := range p.Sources {
		if !ValidURL(src.URL) || src.RetrievedAt <= 0 {
			return errors.New("источник требует ссылки и даты получения")
		}
	}
	return nil
}

func scope(ctx context.Context, q store.Query, actor int64) (pair int64, personal any, key string, err error) {
	if _, err = store.UserFor(ctx, q, actor, false); err != nil {
		return
	}
	pair, err = store.PairFor(ctx, q, actor)
	if err != nil {
		return
	}
	if pair == 0 {
		personal = actor
		key = fmt.Sprintf("user:%d", actor)
	} else {
		key = fmt.Sprintf("pair:%d", pair)
	}
	return
}

func duplicate(ctx context.Context, q store.Query, actor, pair int64, p Input) (int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT id,title,coalesce(address,''),coalesce(map_url,'') FROM places WHERE archived=0 AND (personal_user_id=? OR pair_id=?)", actor, pair)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title, address, link string
		if err = rows.Scan(&id, &title, &address, &link); err != nil {
			return 0, err
		}
		sameLink := p.MapURL != "" && canonical(p.MapURL) == canonical(link)
		sameAddress := strings.TrimSpace(p.Address) != "" && strings.EqualFold(strings.TrimSpace(p.Address), strings.TrimSpace(address)) && strings.EqualFold(strings.TrimSpace(p.Title), strings.TrimSpace(title))
		if sameLink || sameAddress {
			return id, nil
		}
	}
	return 0, rows.Err()
}
func canonical(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	v := u.Query()
	for k := range v {
		if strings.HasPrefix(k, "utm_") {
			v.Del(k)
		}
	}
	u.RawQuery = v.Encode()
	return strings.TrimSuffix(u.String(), "/")
}

// Match includes archived cards so web search cannot silently reintroduce an
// archived/refused/frozen place as a fresh ID. All reads stay in the actor scope.
func (s *Service) Match(ctx context.Context, q store.Query, actor int64, p Input) (*Card, error) {
	pair, _, _, err := scope(ctx, q, actor)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT id,title,coalesce(address,''),coalesce(map_url,'') FROM places WHERE personal_user_id=? OR pair_id=? ORDER BY id", actor, pair)
	if err != nil {
		return nil, err
	}
	var matched int64
	for rows.Next() {
		var id int64
		var title, address, link string
		if err = rows.Scan(&id, &title, &address, &link); err != nil {
			break
		}
		sameLink := p.MapURL != "" && canonical(p.MapURL) == canonical(link)
		sameAddress := strings.TrimSpace(p.Address) != "" && strings.EqualFold(strings.TrimSpace(p.Title), strings.TrimSpace(title)) && strings.EqualFold(strings.TrimSpace(p.Address), strings.TrimSpace(address))
		if sameLink || sameAddress {
			matched = id
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if matched == 0 {
		return nil, nil
	}
	c, err := s.Get(ctx, q, actor, matched)
	return &c, err
}

func (s *Service) Create(ctx context.Context, q store.Query, actor int64, p Input) (int64, error) {
	if err := Validate(p); err != nil {
		return 0, err
	}
	pair, personal, key, err := scope(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	id, err := duplicate(ctx, q, actor, pair, p)
	if err != nil {
		return 0, err
	}
	if id != 0 {
		return 0, &Duplicate{ID: id}
	}
	var pairArg any
	if pair != 0 {
		pairArg = pair
	}
	sources, _ := json.Marshal(p.Sources)
	photos, _ := json.Marshal(p.Photos)
	r, err := q.ExecContext(ctx, `INSERT INTO places(author_id,personal_user_id,pair_id,title,address,city,map_url,description,price,price_basis,sources,photos,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, actor, personal, pairArg, strings.TrimSpace(p.Title), p.Address, p.City, p.MapURL, p.Description, p.Price, nullString(p.PriceBasis), string(sources), string(photos), s.Store.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err = r.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err = categories(ctx, q, id, key, p.Categories); err != nil {
		return 0, err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO place_preferences(place_id,user_id,interest) VALUES(?,?,'want')", id, actor)
	return id, err
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func categories(ctx context.Context, q store.Query, id int64, key string, names []string) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM place_categories WHERE place_id=?", id); err != nil {
		return err
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO categories(scope,title) VALUES(?,?)", key, name); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO place_categories(place_id,category_id) SELECT ?,id FROM categories WHERE scope=? AND title=?", id, key, name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Get(ctx context.Context, q store.Query, actor, id int64) (Card, error) {
	var c Card
	if err := store.PlaceFor(ctx, q, actor, id); err != nil {
		return c, err
	}
	var author sql.NullInt64
	var price sql.NullFloat64
	var sources, photos string
	err := q.QueryRowContext(ctx, `SELECT p.id,p.author_id,coalesce(u.notification_name,'Удалённый пользователь'),p.title,coalesce(p.address,''),coalesce(p.city,''),coalesce(p.map_url,''),p.description,p.price,coalesce(p.price_basis,''),p.sources,p.photos,p.version,p.archived FROM places p LEFT JOIN users u ON u.id=p.author_id WHERE p.id=?`, id).Scan(&c.ID, &author, &c.Author, &c.Title, &c.Address, &c.City, &c.MapURL, &c.Description, &price, &c.PriceBasis, &sources, &photos, &c.Version, &c.Archived)
	if err != nil {
		return c, err
	}
	if author.Valid {
		c.AuthorID = &author.Int64
	}
	if price.Valid {
		c.Price = &price.Float64
	}
	if err = json.Unmarshal([]byte(sources), &c.Sources); err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(photos), &c.Photos); err != nil {
		return c, err
	}
	rows, err := q.QueryContext(ctx, "SELECT c.title FROM place_categories pc JOIN categories c ON c.id=pc.category_id WHERE pc.place_id=? ORDER BY c.title", id)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return c, err
		}
		c.Categories = append(c.Categories, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	if err = q.QueryRowContext(ctx, `SELECT coalesce((SELECT interest FROM place_preferences WHERE place_id=? AND user_id=?),''),coalesce((SELECT interest FROM place_preferences WHERE place_id=? AND user_id<>? AND user_id IN (SELECT user_id FROM pair_members WHERE active=1 AND pair_id=(SELECT pair_id FROM places WHERE id=?)) LIMIT 1),''),EXISTS(SELECT 1 FROM place_preferences WHERE place_id=? AND refused=1),EXISTS(SELECT 1 FROM visit_items vi JOIN visits v ON v.id=vi.visit_id WHERE vi.place_id=? AND v.valid=1)`, id, actor, id, actor, id, id, id).Scan(&c.Mine, &c.Theirs, &c.Refused, &c.Visited); err != nil {
		return c, err
	}
	c.FreezeUntil, err = s.freeze(ctx, q, id)
	return c, err
}

func (s *Service) freeze(ctx context.Context, q store.Query, id int64) (float64, error) {
	var latest, created sql.NullFloat64
	err := q.QueryRowContext(ctx, `SELECT v.visited_at,v.created_at FROM visits v JOIN visit_items vi ON vi.visit_id=v.id WHERE vi.place_id=? AND v.valid=1 AND v.automatic_freeze=1 AND v.visited_at IS NOT NULL ORDER BY v.visited_at DESC,v.id DESC LIMIT 1`, id).Scan(&latest, &created)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var until sql.NullFloat64
	var changed float64
	err = q.QueryRowContext(ctx, "SELECT until_at,changed_at FROM freeze_overrides WHERE place_id=?", id).Scan(&until, &changed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		if until.Valid && until.Float64 > float64(s.Store.Now().Unix()) {
			return until.Float64, nil
		}
		if !until.Valid && (!created.Valid || created.Float64 <= changed) {
			return 0, nil
		}
	}
	if latest.Valid {
		return latest.Float64 + 30*86400, nil
	}
	return 0, nil
}

type Edit struct {
	ID      int64 `json:"id"`
	Version int64 `json:"version"`
	Card    Input `json:"card"`
}

func (s *Service) Edit(ctx context.Context, q store.Query, actor int64, p Edit) error {
	if err := Validate(p.Card); err != nil {
		return err
	}
	old, err := s.Get(ctx, q, actor, p.ID)
	if err != nil {
		return err
	}
	if old.Version != p.Version {
		return store.ErrConflict
	}
	pair, _, key, err := scope(ctx, q, actor)
	if err != nil {
		return err
	}
	dup, err := duplicate(ctx, q, actor, pair, p.Card)
	if err != nil {
		return err
	}
	if dup != 0 && dup != p.ID {
		return &Duplicate{ID: dup}
	}
	src, _ := json.Marshal(p.Card.Sources)
	photos, _ := json.Marshal(p.Card.Photos)
	_, err = q.ExecContext(ctx, `UPDATE places SET title=?,address=?,city=?,map_url=?,description=?,price=?,price_basis=?,sources=?,photos=?,version=version+1 WHERE id=? AND version=?`, strings.TrimSpace(p.Card.Title), p.Card.Address, p.Card.City, p.Card.MapURL, p.Card.Description, p.Card.Price, nullString(p.Card.PriceBasis), string(src), string(photos), p.ID, p.Version)
	if err != nil {
		return err
	}
	return categories(ctx, q, p.ID, key, p.Card.Categories)
}

type Archive struct {
	ID       int64 `json:"id"`
	Version  int64 `json:"version"`
	Archived bool  `json:"archived"`
}

func (s *Service) Archive(ctx context.Context, q store.Query, actor int64, p Archive) error {
	c, err := s.Get(ctx, q, actor, p.ID)
	if err != nil {
		return err
	}
	if c.Version != p.Version {
		return store.ErrConflict
	}
	_, err = q.ExecContext(ctx, "UPDATE places SET archived=?,version=version+1 WHERE id=?", p.Archived, p.ID)
	return err
}

type Interest struct {
	ID       int64  `json:"id"`
	Interest string `json:"interest"`
}

func (s *Service) Interest(ctx context.Context, q store.Query, actor int64, p Interest) error {
	if p.Interest != "want" && p.Interest != "uninterested" && p.Interest != "" {
		return errors.New("неизвестная оценка")
	}
	if err := store.PlaceFor(ctx, q, actor, p.ID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO place_preferences(place_id,user_id,interest) VALUES(?,?,?) ON CONFLICT(place_id,user_id) DO UPDATE SET interest=excluded.interest`, p.ID, actor, nullString(p.Interest))
	return err
}

func (s *Service) List(ctx context.Context, actor int64, filter string) ([]Card, error) {
	pair, _, _, err := scope(ctx, s.Store.DB, actor)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT id FROM places WHERE personal_user_id=? OR pair_id=? ORDER BY id DESC", actor, pair)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	cards := []Card{}
	for _, id := range ids {
		c, err := s.Get(ctx, s.Store.DB, actor, id)
		if err != nil {
			return nil, err
		}
		if c.Archived && filter != "archive" {
			continue
		}
		take := false
		switch filter {
		case "all":
			take = true
		case "mine":
			take = c.AuthorID != nil && *c.AuthorID == actor
		case "partner":
			take = c.AuthorID != nil && *c.AuthorID != actor
		case "mutual":
			take = c.Mine == "want" && c.Theirs == "want"
		case "frozen":
			take = c.FreezeUntil > float64(s.Store.Now().Unix())
		case "visited":
			take = c.Visited
		case "archive":
			take = c.Archived
		default:
			return nil, errors.New("неизвестный фильтр")
		}
		if take {
			cards = append(cards, c)
		}
	}
	return cards, nil
}

func Render(c Card) string {
	text := c.Title + "\nКатегории: " + strings.Join(c.Categories, ", ") + "\nДобавил: " + c.Author
	if c.Address != "" {
		text += "\nАдрес: " + c.Address
	} else {
		text += "\nАдрес не указан"
	}
	if c.MapURL != "" {
		text += "\nКарта: " + c.MapURL
	}
	if c.Description != "" {
		text += "\n" + c.Description
	}
	if c.Price != nil {
		basis := "на человека"
		if c.PriceBasis == "couple" {
			basis = "на двоих"
		}
		text += "\nЦена: " + strconv.FormatFloat(*c.Price, 'f', -1, 64) + " ₽ " + basis
	} else {
		text += "\nБюджет не проверен"
	}
	text += "\nЧасы работы не проверены"
	for _, src := range c.Sources {
		text += "\nИсточник: " + src.URL + " (" + time.Unix(src.RetrievedAt, 0).UTC().Format("2006-01-02") + ")"
	}
	if c.Refused {
		text += "\nОдин из вас больше не хочет сюда. Ручной выбор доступен."
	}
	if c.FreezeUntil > 0 {
		text += "\nФриз до " + time.Unix(int64(c.FreezeUntil), 0).UTC().Format("2006-01-02")
	}
	if c.Theirs == "" {
		text += "\nПартнёр пока не оценил"
	}
	if c.Archived {
		text += "\nВ архиве"
	}
	return text
}
