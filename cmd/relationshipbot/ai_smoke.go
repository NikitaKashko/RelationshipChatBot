package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/config"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

// Synthetic integration check: temporary database, no actual Telegram users or
// collection data. It makes paid API calls only when explicitly invoked.
func checkAIFlow(ctx context.Context, cfg config.Config) error {
	dir, err := os.MkdirTemp("", "relationship-ai-smoke-")
	if err != nil {
		return fmt.Errorf("smoke workspace failed")
	}
	// Only remove the exact newly created directory within the OS temp root.
	if rel, e := filepath.Rel(os.TempDir(), dir); e != nil || rel != filepath.Base(dir) || !strings.HasPrefix(rel, "relationship-ai-smoke-") {
		return fmt.Errorf("unsafe smoke workspace path")
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, filepath.Join(dir, "fixture.sqlite"))
	if err != nil {
		return fmt.Errorf("smoke database failed")
	}
	defer db.DB.Close()
	db.Now = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }
	_, err = db.DB.ExecContext(ctx, `INSERT INTO users(id,name,code,status,city,timezone,created_at) VALUES(1,'Synthetic A','SMOKE-A','ready','Москва','Europe/Moscow',1),(2,'Synthetic B','SMOKE-B','ready','Москва','Europe/Moscow',1);
 INSERT INTO pairs(id,city,timezone,created_at) VALUES(1,'Москва','Europe/Moscow',1);
 INSERT INTO pair_members(pair_id,user_id) VALUES(1,1),(1,2);
 INSERT INTO consents(user_id,kind,enabled,updated_at) VALUES(1,'ai',1,1),(2,'ai',1,1);
 INSERT INTO places(id,author_id,pair_id,title,price,price_basis,created_at) VALUES(1,1,1,'Прогулка в парке (тестовая идея)',800,'couple',1);
 INSERT INTO categories(id,scope,title) VALUES(1,'pair:1','Погулять');
 INSERT INTO place_categories(place_id,category_id) VALUES(1,1);`)
	if err != nil {
		return fmt.Errorf("smoke fixture failed")
	}
	s := ai.Service{Store: db, Model: cfg.DeepSeekModel, Provider: &ai.DeepSeek{Key: cfg.DeepSeekKey, Model: cfg.DeepSeekModel}}
	result, err := s.Converse(ctx, 1, "Подготовь свидание завтра с 18:00 до 20:00 в Москве, до 1000 рублей на двоих. Выбери из нашей коллекции единственное подходящее место. Дополнительных мест не нужно. Только подготовь черновик плана.")
	if err != nil || result.Retry != "" {
		return fmt.Errorf("AI flow failed")
	}
	plans := 0
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM plans").Scan(&plans)
	if plans != 0 {
		return fmt.Errorf("AI flow committed without confirmation")
	}
	confirmed := false
	for _, draft := range result.Drafts {
		var kind, raw string
		if err = db.DB.QueryRowContext(ctx, "SELECT kind,payload FROM drafts WHERE id=?", draft.ID).Scan(&kind, &raw); err != nil {
			return fmt.Errorf("smoke draft unavailable")
		}
		if kind != "ai:plan_save" {
			continue
		}
		var p struct {
			Start  string  `json:"start"`
			End    string  `json:"end"`
			Places []int64 `json:"places"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil {
			return fmt.Errorf("invalid smoke draft")
		}
		if len(p.Places) == 1 && p.Places[0] == 1 && len(p.Start) >= 10 && p.Start[:10] == "2026-10-12" {
			confirmed = true
		}
	}
	if !confirmed {
		fmt.Printf("Synthetic diagnostic: draft_count=%d\n", len(result.Drafts))
		return fmt.Errorf("AI flow did not prepare expected dated draft")
	}
	var count int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM api_usage").Scan(&count)
	fmt.Printf("AI_FLOW_OK dated_draft=true committed=false API_calls=%d synthetic_only=true\n", count)
	return nil
}
