package texts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogCanChangeWithoutCodeAndValidatesRequiredCopy(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var file fileCatalog
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	key := "internal.telegram.menu.001"
	file.Texts[key] = "🏡 Другой текст меню"
	changed, _ := json.Marshal(file)
	path := filepath.Join(t.TempDir(), "ru.json")
	if err = os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Read(path)
	if err != nil || catalog.T(key) != file.Texts[key] {
		t.Fatal("runtime text edit was not loaded", err)
	}

	delete(file.Texts, key)
	missing, _ := json.Marshal(file)
	if err = os.WriteFile(path, missing, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(path); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatal("missing key accepted", err)
	}
}

func TestCatalogRejectsDuplicateKeysAndChangedPlaceholders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ru.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"version":1,"texts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatal("duplicate key accepted", err)
	}

	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var file fileCatalog
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	key := "internal.affection.service.002"
	file.Texts[key] = "Имя потеряло обязательный параметр"
	changed, _ := json.Marshal(file)
	if err = os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(path); err == nil || !strings.Contains(err.Error(), "placeholders changed") {
		t.Fatal("placeholder change accepted", err)
	}
}
