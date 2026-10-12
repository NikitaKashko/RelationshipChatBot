// Package texts loads editable application copy from disk.
package texts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const relativeCatalog = "content/ru.json"

type fileCatalog struct {
	Version int               `json:"version"`
	Texts   map[string]string `json:"texts"`
}

type Catalog struct {
	path  string
	texts map[string]string
}

var (
	defaultOnce sync.Once
	defaultCat  *Catalog
	defaultErr  error
)

// Read validates and loads a catalog without changing the process default.
func Read(path string) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read text catalog %q: %w", path, err)
	}
	if err = rejectDuplicateJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("text catalog %q: %w", path, err)
	}
	var file fileCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse text catalog %q: %w", path, err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("text catalog %q: unsupported version %d", path, file.Version)
	}
	if len(file.Texts) == 0 {
		return nil, fmt.Errorf("text catalog %q: texts are empty", path)
	}
	missing := make([]string, 0)
	for key, signature := range requiredFormats {
		value, ok := file.Texts[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("text catalog %q: %s is empty", path, key)
		}
		if got := formatSignature(value); got != signature {
			return nil, fmt.Errorf("text catalog %q: %s placeholders changed: want %q, got %q", path, key, signature, got)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("text catalog %q: missing required keys: %s", path, strings.Join(missing, ", "))
	}
	return &Catalog{path: path, texts: file.Texts}, nil
}

func (c *Catalog) T(key string) string {
	if c == nil {
		panic("text catalog is nil")
	}
	value, ok := c.texts[key]
	if !ok {
		panic("text catalog key is missing: " + key)
	}
	return value
}

func (c *Catalog) Path() string { return c.path }

// Validate eagerly loads the process catalog. Commands call this before doing
// any work so a broken content release fails without touching application data.
func Validate() error {
	defaultOnce.Do(func() {
		path, err := discoverPath()
		if err != nil {
			defaultErr = err
			return
		}
		defaultCat, defaultErr = Read(path)
	})
	return defaultErr
}

// T returns one required text. It panics only when startup validation was
// skipped by a programmer; production commands validate before application IO.
func T(key string) string {
	if err := Validate(); err != nil {
		panic(err)
	}
	return defaultCat.T(key)
}

func Path() string {
	if err := Validate(); err != nil {
		return ""
	}
	return defaultCat.Path()
}

func Lookup(key string) (string, bool) {
	if err := Validate(); err != nil {
		return "", false
	}
	value, ok := defaultCat.texts[key]
	return value, ok
}

func Count() int {
	if err := Validate(); err != nil {
		return 0
	}
	return len(defaultCat.texts)
}

func discoverPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("BOT_TEXTS_FILE")); explicit != "" {
		return explicit, nil
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), relativeCatalog)
		if _, err = os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, relativeCatalog)
		if _, err = os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("text catalog not found; set BOT_TEXTS_FILE or install %s beside the executable", filepath.FromSlash(relativeCatalog))
}

func formatSignature(value string) string {
	var signature strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			continue
		}
		if i+1 < len(value) && value[i+1] == '%' {
			i++
			continue
		}
		for i++; i < len(value); i++ {
			r := rune(value[i])
			if unicode.IsLetter(r) {
				signature.WriteByte('%')
				signature.WriteRune(r)
				break
			}
		}
	}
	return signature.String()
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
