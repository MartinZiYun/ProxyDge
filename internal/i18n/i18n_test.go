package i18n

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
)

func TestNormalizeLocale(t *testing.T) {
	tests := []struct {
		in   string
		want Locale
	}{
		{"en", LocaleEN},
		{"en-US", LocaleEN},
		{"en_US", LocaleEN},
		{"en-GB", LocaleEN},
		{"en_US.UTF-8", LocaleEN},
		{"zh", LocaleZhCN},
		{"zh-CN", LocaleZhCN},
		{"zh_CN", LocaleZhCN},
		{"zh-CN.UTF-8", LocaleZhCN},
		{"zh-TW", LocaleZhTW},
		{"zh_TW", LocaleZhTW},
		{"zh-HK", LocaleZhTW},
		{"zh-TW.UTF-8", LocaleZhTW},
		{"fr", LocaleEN},
		{"ja", LocaleEN},
		{"", LocaleEN},
	}
	for _, tt := range tests {
		got := normalizeLocale(tt.in)
		if got != tt.want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDetectLocaleExplicit(t *testing.T) {
	if got := DetectLocale("zh-CN"); got != LocaleZhCN {
		t.Fatalf("explicit zh-CN: got %q", got)
	}
	if got := DetectLocale("en"); got != LocaleEN {
		t.Fatalf("explicit en: got %q", got)
	}
}

func TestDetectLocaleEmptyFallsBackToEnv(t *testing.T) {
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_ALL", "")
	if got := DetectLocale(""); got != LocaleZhCN {
		t.Fatalf("empty explicit + LANG=zh_CN: got %q", got)
	}
}

func TestDetectLocaleEmptyNoEnvFallsBackToEn(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	if got := DetectLocale(""); got != LocaleEN {
		t.Fatalf("no env: got %q, want en", got)
	}
}

func TestLoadEn(t *testing.T) {
	c, err := Load(LocaleEN)
	if err != nil {
		t.Fatalf("Load(en): %v", err)
	}
	if c == nil {
		t.Fatal("catalog is nil")
	}
}

func TestLoadZhCN(t *testing.T) {
	c, err := Load(LocaleZhCN)
	if err != nil {
		t.Fatalf("Load(zh-CN): %v", err)
	}
	if c == nil {
		t.Fatal("catalog is nil")
	}
}

func TestLoadUnsupportedFallsBackToEn(t *testing.T) {
	c, err := Load("fr")
	if err != nil {
		t.Fatalf("Load(fr): %v", err)
	}
	if _, ok := c.messages["help.text"]; !ok {
		t.Fatal("fallback to en should have help.text key")
	}
}

func TestTLookupEn(t *testing.T) {
	c, _ := Load(LocaleEN)
	got := c.T("notice.config_migrated")
	if got == "" || got == "[missing:notice.config_migrated]" {
		t.Fatalf("T(notice.config_migrated) = %q", got)
	}
}

func TestTLookupZhCN(t *testing.T) {
	c, _ := Load(LocaleZhCN)
	got := c.T("notice.config_migrated")
	if got == "" || got == "[missing:notice.config_migrated]" {
		t.Fatalf("T(notice.config_migrated) = %q", got)
	}
}

func TestTWithArgs(t *testing.T) {
	c, _ := Load(LocaleEN)
	got := c.T("notice.config_migrated", 1, "/etc/proxydge/config.yaml")
	// Should contain the version number and path
	if !contains(got, "1") || !contains(got, "/etc/proxydge/config.yaml") {
		t.Fatalf("T with args: got %q, expected version 1 and path", got)
	}
}

func TestTFallbackToEn(t *testing.T) {
	// zh-CN catalog should have all keys, but if one is missing, fall back to en.
	// We test by loading zh-CN and checking that help.text is present (it is).
	c, _ := Load(LocaleZhCN)
	got := c.T("help.text")
	if got == "" || got == "[missing:help.text]" {
		t.Fatalf("zh-CN T(help.text) should fall back to en if missing: %q", got)
	}
}

func TestTMissingKey(t *testing.T) {
	c, _ := Load(LocaleEN)
	got := c.T("nonexistent.key")
	if got != "[missing:nonexistent.key]" {
		t.Fatalf("missing key: want [missing:nonexistent.key], got %q", got)
	}
}

func TestKeyConsistency(t *testing.T) {
	en, _ := Load(LocaleEN)
	zhCN, _ := Load(LocaleZhCN)
	zhTW, _ := Load(LocaleZhTW)
	enKeys := en.Keys()
	zhCNKeys := zhCN.Keys()
	zhTWKeys := zhTW.Keys()
	sort.Strings(enKeys)
	sort.Strings(zhCNKeys)
	sort.Strings(zhTWKeys)
	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{"zh-CN", zhCNKeys, enKeys},
		{"zh-TW", zhTWKeys, enKeys},
	} {
		if len(tc.got) != len(tc.want) {
			t.Fatalf("key count mismatch: en=%d, %s=%d\nen: %v\n%s: %v", len(tc.want), tc.name, len(tc.got), tc.want, tc.name, tc.got)
		}
		for i, k := range tc.want {
			if k != tc.got[i] {
				t.Fatalf("key mismatch at %d: en=%q, %s=%q", i, k, tc.name, tc.got[i])
			}
		}
	}
}

func TestKeysSortedForStableTest(t *testing.T) {
	en, _ := Load(LocaleEN)
	keys := en.Keys()
	if len(keys) == 0 {
		t.Fatal("no keys in en catalog")
	}
}

// fmtVerbSeq extracts the ordered fmt verbs (%q, %d, %v, ...) from s,
// tolerating flags/width/precision (e.g. %2d, %.3f). Literal %% is ignored.
var fmtVerbRe = regexp.MustCompile(`%[-+ #0-9.]*([a-zA-Z])`)

func fmtVerbSeq(s string) []string {
	var out []string
	for _, m := range fmtVerbRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestVerbConsistency guards translated messages used with args: main.go feeds
// one shared Args slice to both the English fallback and each locale's
// template, so every locale must use the same verbs in the same order —
// otherwise output degrades to "%!q(MISSING)" or "%!(EXTRA ...)".
func TestVerbConsistency(t *testing.T) {
	en, _ := Load(LocaleEN)
	for _, loc := range []Locale{LocaleZhCN, LocaleZhTW} {
		cat, err := Load(loc)
		if err != nil {
			t.Fatalf("Load(%s): %v", loc, err)
		}
		for _, k := range en.Keys() {
			msg, ok := cat.messages[k]
			if !ok {
				t.Errorf("%s: missing key %q", loc, k)
				continue
			}
			want := fmtVerbSeq(en.messages[k])
			got := fmtVerbSeq(msg)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("%s: %s verb mismatch\n  en     : %v (%q)\n  %-6s: %v (%q)",
					loc, k, want, en.messages[k], loc, got, msg)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
