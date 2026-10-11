// Package config reads local development settings; environment takes priority.
// Never include Config, tokens or HTTP errors containing URLs in logs.
package config

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

type Config struct{ BotToken, DeepSeekKey, DeepSeekModel, DatabasePath string }

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
	return Config{BotToken: get("BOT_TOKEN", ""), DeepSeekKey: get("DEEPSEEK_API_KEY", ""), DeepSeekModel: get("DEEPSEEK_MODEL", "deepseek-flash"), DatabasePath: get("DATABASE_PATH", "data/release.sqlite")}, nil
}
