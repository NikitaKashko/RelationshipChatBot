package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironmentPriorityAndSafeErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("BOT_TOKEN=file-secret\nDEEPSEEK_API_KEY='test-secret'\n"), 0600)
	t.Setenv("BOT_TOKEN", "env-secret")
	c, err := Load(p)
	if err != nil || c.BotToken != "env-secret" || c.DeepSeekKey != "test-secret" {
		t.Fatal("wrong configuration")
	}
	os.WriteFile(p, []byte("test-secret-without-equals"), 0600)
	_, err = Load(p)
	if err == nil || err.Error() != "invalid configuration line" {
		t.Fatal("unsafe error")
	}
}
