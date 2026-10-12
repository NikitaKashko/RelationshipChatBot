// Package config reads local development settings; environment takes priority.
// Never include Config, tokens or HTTP errors containing URLs in logs.
package config

import (
	"bufio"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken, DeepSeekKey, DeepSeekModel, DatabasePath string
	USDRUB, BudgetTargetRUB                            float64
	PanelPassword, BackupDir, BotUsername              string
	TestBotToken, TestDatabasePath                     string
	TestOwnerID                                        int64
}

func Load(path string) (Config, error) {
	values := map[string]string{}
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return Config{}, errors.New("invalid configuration line")
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
				v = v[1 : len(v)-1]
			}
			values[strings.TrimSpace(k)] = v
		}
		if err = scanner.Err(); err != nil {
			return Config{}, errors.New("cannot read configuration")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, errors.New("cannot open configuration")
	}
	get := func(k, fallback string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		if v, ok := values[k]; ok {
			return v
		}
		return fallback
	}
	fx, err := strconv.ParseFloat(get("BILLING_USD_RUB", "0"), 64)
	if err != nil || fx < 0 || math.IsNaN(fx) || math.IsInf(fx, 0) {
		return Config{}, errors.New("invalid billing exchange rate")
	}
	target, err := strconv.ParseFloat(get("BILLING_TARGET_RUB", "0"), 64)
	if err != nil || target < 0 || math.IsNaN(target) || math.IsInf(target, 0) {
		return Config{}, errors.New("invalid billing target")
	}
	owner, err := strconv.ParseInt(get("TEST_OWNER_ID", "0"), 10, 64)
	if err != nil || owner < 0 {
		return Config{}, errors.New("invalid test owner ID")
	}
	return Config{BotToken: get("BOT_TOKEN", ""), DeepSeekKey: get("DEEPSEEK_API_KEY", ""), DeepSeekModel: get("DEEPSEEK_MODEL", "deepseek-flash"), DatabasePath: get("DATABASE_PATH", "data/release.sqlite"), USDRUB: fx, BudgetTargetRUB: target, PanelPassword: get("PANEL_PASSWORD", ""), BackupDir: get("BACKUP_DIR", "data/backups"), BotUsername: get("BOT_USERNAME", "relation_support_bot"), TestBotToken: get("TEST_BOT_TOKEN", ""), TestDatabasePath: get("TEST_DATABASE_PATH", "data/sandbox.sqlite"), TestOwnerID: owner}, nil
}
