package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// lastComponent returns the part after the last dot: "tcp.detect-timeout" → "detect-timeout".
func lastComponent(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// firstComponent returns the part before the first dot: "tcp.detect-timeout" → "tcp".
func firstComponent(name string) string {
	if i := strings.Index(name, "."); i >= 0 {
		return name[:i]
	}
	return name
}

// TestFieldRegistryConsistency verifies the full chain:
//
//	configFields → Config (can hold)
//	          → sampleConfig (contains field name)
//	          → generateMigratedConfig (generates field)
//	          → knownConfigKeys (recognizes top-level key)
//	          → defaultsSource (default value matches defVal)
//
// Adding a field without updating all five downstream artifacts causes this
// test to fail in CI — not at runtime when a user discovers the gap.
func TestFieldRegistryConsistency(t *testing.T) {
	// 1. Every configField value getter works on a zero Config.
	var c Config
	for _, f := range configFields {
		_ = f.value(&c) // must not panic
	}

	// 2. Every field name appears in sampleConfig (by last YAML key component).
	for _, f := range configFields {
		key := lastComponent(f.name)
		if !strings.Contains(sampleConfig, key) {
			t.Errorf("sampleConfig missing field %q (looking for %q)", f.name, key)
		}
	}

	// 3. Every field name appears in generateMigratedConfig output.
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 0\nupstream: 1.2.3.4:80\n")
	var c2 Config
	if err := (fileSource{path: p}).Apply(&c2); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	migrated, _ := os.ReadFile(p)
	migratedStr := string(migrated)
	for _, f := range configFields {
		key := lastComponent(f.name)
		if !strings.Contains(migratedStr, key) {
			t.Errorf("generateMigratedConfig missing field %q (looking for %q)", f.name, key)
		}
	}

	// 4. Every field's top-level key is in knownConfigKeys.
	for _, f := range configFields {
		top := firstComponent(f.name)
		if !knownConfigKeys[top] {
			t.Errorf("knownConfigKeys missing top-level key %q for field %q", top, f.name)
		}
	}

	// 5. defaultsSource values match registry defVal.
	var c3 Config
	(defaultsSource{}).Apply(&c3)
	for _, f := range configFields {
		got := f.value(&c3)
		if !reflect.DeepEqual(got, f.defVal) {
			t.Errorf("field %q: defaultsSource gives %v, registry says %v", f.name, got, f.defVal)
		}
	}
}
