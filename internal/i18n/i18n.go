// Package i18n provides lightweight internationalization for ProxyDge CLI
// output. Locale files are embedded into the binary via go:embed, keeping
// the single-file release. Business packages (config, gateway) use message
// keys, not hardcoded text; main translates keys via Catalog.T.
package i18n

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Locale is a normalized language tag ("en", "zh-CN", or "zh-TW").
type Locale string

const (
	LocaleEN   Locale = "en"
	LocaleZhCN Locale = "zh-CN"
	LocaleZhTW Locale = "zh-TW"
)

//go:embed locales/en.yaml
var localeEN []byte

//go:embed locales/zh-CN.yaml
var localeZhCN []byte

//go:embed locales/zh-TW.yaml
var localeZhTW []byte

var localeData = map[Locale][]byte{
	LocaleEN:   localeEN,
	LocaleZhCN: localeZhCN,
	LocaleZhTW: localeZhTW,
}

// DetectLocale resolves the active locale. If explicit is non-empty (from
// --lang flag or PROXYDGE_LANG env), it wins. Otherwise the system locale
// (LANG / LC_ALL) is checked. Anything unrecognized falls back to English.
func DetectLocale(explicit string) Locale {
	if explicit != "" {
		return normalizeLocale(explicit)
	}
	for _, env := range []string{"LANG", "LC_ALL"} {
		if v := os.Getenv(env); v != "" {
			return normalizeLocale(v)
		}
	}
	return LocaleEN
}

// normalizeLocale maps common locale strings to our supported set.
//   - en, en-US, en_US, en-GB → "en"
//   - zh, zh-CN, zh_CN → "zh-CN" (Simplified Chinese)
//   - zh-TW, zh_TW, zh-HK → "zh-TW" (Traditional Chinese)
//   - everything else → "en" (fallback)
func normalizeLocale(s string) Locale {
	s = strings.TrimSpace(s)
	// Strip encoding suffix: en_US.UTF-8 → en_US
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	lower := strings.ToLower(strings.ReplaceAll(s, "_", "-"))
	if lower == "en" || strings.HasPrefix(lower, "en-") {
		return LocaleEN
	}
	if lower == "zh-tw" || lower == "zh-hk" {
		return LocaleZhTW
	}
	if lower == "zh" || lower == "zh-cn" || strings.HasPrefix(lower, "zh-") {
		return LocaleZhCN
	}
	return LocaleEN
}

// Catalog holds a set of translated messages. When a key is missing, it
// falls back to the English catalog (if non-nil). If English also lacks
// the key, T returns a visible "[missing:key]" string — never panics.
type Catalog struct {
	messages map[string]string
	fallback *Catalog // English fallback (nil for the English catalog itself)
}

// Load parses and returns the catalog for locale. If the locale is not
// supported (should not happen after normalization), English is loaded
// instead.
func Load(locale Locale) (*Catalog, error) {
	data, ok := localeData[locale]
	if !ok {
		locale = LocaleEN
		data = localeData[LocaleEN]
	}
	var msgs map[string]string
	if err := yaml.Unmarshal(data, &msgs); err != nil {
		return nil, fmt.Errorf("parse locale %s: %w", locale, err)
	}
	c := &Catalog{messages: msgs}
	if locale != LocaleEN {
		c.fallback = LoadEN()
	}
	return c, nil
}

// LoadEN returns the English catalog. It never fails — embedded data is
// compiled in. If the embedded YAML is somehow malformed, an empty catalog
// is returned (T returns "[missing:key]" for every key, but the program
// still starts).
func LoadEN() *Catalog {
	c, err := Load(LocaleEN)
	if err != nil {
		return &Catalog{messages: map[string]string{}}
	}
	return c
}

// T looks up a translated message by key. If the key is missing in the
// current locale, it falls back to English. If English also lacks the key,
// it returns "[missing:key]". If args are provided, they are passed to
// fmt.Sprintf for interpolation.
func (c *Catalog) T(key string, args ...any) string {
	msg, ok := c.messages[key]
	if !ok && c.fallback != nil {
		return c.fallback.T(key, args...)
	}
	if !ok {
		return "[missing:" + key + "]"
	}
	if len(args) > 0 {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}

// Keys returns the set of message keys in this catalog. Used by tests to
// verify that en and zh-CN have the same key set.
func (c *Catalog) Keys() []string {
	keys := make([]string, 0, len(c.messages))
	for k := range c.messages {
		keys = append(keys, k)
	}
	return keys
}
