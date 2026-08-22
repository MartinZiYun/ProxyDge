// Package config centralizes ProxyDge configuration sourcing and validation.
//
// Configuration is assembled from independent Sources, each of which overlays
// only the fields it actually provides onto a single shared Config. Sources do
// not know about each other. Precedence is purely the order of application in
// Load: defaults (lowest) -> file -> env -> flags (highest). Defaults is itself
// a Source, so Load is uniform. Validation is centralized in Config.Validate
// and never scattered across the flag parser.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime configuration. It is the single object every Source
// overlays onto; it is also what the rest of the program consumes. main maps
// its fields into gateway.New — the gateway never imports this package.
type Config struct {
	Listen               string
	Upstream             string
	Policy               string
	DetectTimeout        time.Duration
	Lang                 string // display language: "" (auto) | "en" | "zh-CN"
	TrustedNetworks      []string
	UntrustedProxyAction string
	Protocol             string        // "tcp" (default) | "udp"
	IdleTimeout          time.Duration // UDP session idle timeout (default 30s)
	MaxSessions          int           // max concurrent UDP sessions (default 1024)
	MaxDatagramSize      int           // max datagram size, oversized=drop (default 65535)
	UDPOutput            string        // "every_datagram" (default) | "first_datagram"
	ConfigPath           string        // resolved config file path (meta; not validated)

	// Logging. console and file are independent sinks, each with its own level
	// and format. The file sink is off when LogFilePath == "". main builds a
	// single *slog.Logger that fans out to both; the gateway is sink-agnostic.
	LogConsoleLevel  string
	LogConsoleFormat string
	LogFilePath      string
	LogFileLevel     string
	LogFileFormat    string

	// Diagnostics (not user-configurable). prov maps each field name to the
	// source that last set it ("default" | "env" | "flag" | "file <path>");
	// loadedFile is the config file actually read ("" if none). Printed at
	// startup so an operator can see where every value came from.
	prov       map[string]string
	loadedFile string
	Migrated   bool // true if the config file was auto-migrated on load
}

// currentConfigVersion is the config format version. Bump when new fields are
// added; old configs with a lower version are auto-migrated on load.
const currentConfigVersion = 2

// mark records that source provided field. Sources call this as they overlay,
// so later (higher-precedence) sources overwrite earlier ones — prov[field]
// ends up being the winning source.
func (c *Config) mark(field, source string) {
	if c.prov == nil {
		c.prov = map[string]string{}
	}
	c.prov[field] = source
}

// configField pairs a field's display name with a value accessor and its
// display group, for Describe.
type configField struct {
	name  string
	group string
	value func(*Config) any
}

// configFields is the single source of truth for field names + display order.
// Sources mark fields using these exact names; Describe prints them in order,
// grouped by the group field.
var configFields = []configField{
	{"listen", "connection", func(c *Config) any { return c.Listen }},
	{"upstream", "connection", func(c *Config) any { return c.Upstream }},
	{"policy", "proxy header", func(c *Config) any { return c.Policy }},
	{"detect-timeout", "proxy header", func(c *Config) any { return c.DetectTimeout }},
	{"lang", "proxy header", func(c *Config) any { return c.Lang }},
	{"trusted-networks", "trust", func(c *Config) any { return c.TrustedNetworks }},
	{"untrusted-proxy-action", "trust", func(c *Config) any { return c.UntrustedProxyAction }},
	{"protocol", "connection", func(c *Config) any { return c.Protocol }},
	{"idle-timeout", "udp", func(c *Config) any { return c.IdleTimeout }},
	{"max-sessions", "udp", func(c *Config) any { return c.MaxSessions }},
	{"max-datagram-size", "udp", func(c *Config) any { return c.MaxDatagramSize }},
	{"udp-output", "udp", func(c *Config) any { return c.UDPOutput }},
	{"log.console.level", "logging", func(c *Config) any { return c.LogConsoleLevel }},
	{"log.console.format", "logging", func(c *Config) any { return c.LogConsoleFormat }},
	{"log.file.path", "logging", func(c *Config) any { return c.LogFilePath }},
	{"log.file.level", "logging", func(c *Config) any { return c.LogFileLevel }},
	{"log.file.format", "logging", func(c *Config) any { return c.LogFileFormat }},
}

// fieldName constants keep the Sources' mark() calls aligned with configFields.
const (
	fListen               = "listen"
	fUpstream             = "upstream"
	fPolicy               = "policy"
	fDetectTimeout        = "detect-timeout"
	fLang                 = "lang"
	fTrustedNetworks      = "trusted-networks"
	fUntrustedProxyAction = "untrusted-proxy-action"
	fProtocol             = "protocol"
	fIdleTimeout          = "idle-timeout"
	fMaxSessions          = "max-sessions"
	fMaxDatagramSize      = "max-datagram-size"
	fUDPOutput            = "udp-output"
	fLogConsoleLevel      = "log.console.level"
	fLogConsoleFormat     = "log.console.format"
	fLogFilePath          = "log.file.path"
	fLogFileLevel         = "log.file.level"
	fLogFileFormat        = "log.file.format"
)

// Describe returns a human-readable dump of every config field with its value
// and the source that provided it, plus the config file that was loaded (if
// any). Intended for the startup banner. The file source is shown as just
// "(file)" — the full path is on the "config file:" line above.
func (c *Config) Describe() string {
	var b strings.Builder
	b.WriteString("config file: ")
	if c.loadedFile != "" {
		b.WriteString(c.loadedFile)
	} else {
		b.WriteString("(none)")
	}
	b.WriteString("\n\n-- config --\n")
	for _, f := range configFields {
		fmt.Fprintf(&b, "  %s = %v (%s)\n", f.name, f.value(c), c.sourceOf(f.name))
	}
	b.WriteString("-----------\n")
	return b.String()
}

// sourceOf returns the source label for a field (defaults to "default").
func (c *Config) sourceOf(field string) string {
	if s, ok := c.prov[field]; ok {
		return s
	}
	return "default"
}

// Warning is a security warning with a message key (for i18n) and optional
// args. main translates the key via the i18n catalog; the config package
// itself never formats user-visible text.
type Warning struct {
	Key  string
	Args []any
}

// Warnings returns security warnings for the startup banner. An empty
// trusted-networks and untrusted-proxy-action=strip both produce warnings
// explaining the consequences. Returns message keys (not formatted text) so
// main can translate them.
func (c *Config) Warnings() []Warning {
	var ws []Warning
	if len(c.TrustedNetworks) == 0 {
		ws = append(ws, Warning{Key: "warning.trusted_networks.empty"})
	}
	if c.UntrustedProxyAction == "strip" {
		ws = append(ws, Warning{Key: "warning.untrusted_proxy_action.strip"})
	}
	return ws
}

// MigrationNotice returns a message key + args if the config file was
// auto-migrated on load, or ("", nil) if no migration happened. main
// translates the key via the i18n catalog.
func (c *Config) MigrationNotice() (key string, args []any) {
	if !c.Migrated {
		return "", nil
	}
	return "notice.config_migrated", []any{currentConfigVersion, c.loadedFile}
}

// Source overlays configuration fields it actually provides onto cfg. A Source
// must not clobber fields it does not provide (so that lower-precedence Sources
// survive). Sources never read each other.
type Source interface {
	Apply(cfg *Config) error
}

// ErrHelp signals that the user asked for help (-h); the program should exit 0.
var ErrHelp = errors.New("config: help requested")

// Load resolves configuration from all sources in precedence order and
// validates the result. args are the `proxydge start` CLI args (after the
// "start" subcommand).
func Load(args []string) (*Config, error) {
	cfg := &Config{}

	// 1. defaults (lowest precedence)
	if err := (defaultsSource{}).Apply(cfg); err != nil {
		return nil, err
	}

	// Parse flags once (without applying) so we can surface flag errors early
	// and read -config to locate the file before higher-precedence overlays run.
	fv, set, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, ErrHelp
		}
		return nil, err
	}

	// 2. file: path = -config (explicit, required) else exe-dir/config.yaml
	//    (auto, optional).
	path, optional := resolveConfigPath(set, *fv.config, DefaultConfigPath())
	if path != "" {
		if err := (fileSource{path: path, optional: optional}).Apply(cfg); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
	}

	// 3. env
	if err := (envSource{}).Apply(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	// 4. flags (highest precedence)
	if err := (flagSource{fv: fv, set: set}).Apply(cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate is the single place configuration correctness is checked.
func (c *Config) Validate() error {
	if c.Upstream == "" {
		return errors.New("config: -upstream is required")
	}
	switch c.Policy {
	case "use", "require", "reject":
	default:
		return fmt.Errorf("config: invalid policy %q (use|require|reject)", c.Policy)
	}
	switch c.UntrustedProxyAction {
	case "reject", "strip":
	default:
		return fmt.Errorf("config: invalid untrusted-proxy-action %q (reject|strip)", c.UntrustedProxyAction)
	}
	switch c.Protocol {
	case "tcp", "udp":
	default:
		return fmt.Errorf("config: invalid protocol %q (tcp|udp)", c.Protocol)
	}
	switch c.UDPOutput {
	case "every_datagram", "first_datagram":
	default:
		return fmt.Errorf("config: invalid udp-output %q (every_datagram|first_datagram)", c.UDPOutput)
	}
	if c.MaxSessions <= 0 {
		return fmt.Errorf("config: max-sessions must be > 0, got %d", c.MaxSessions)
	}
	if c.MaxDatagramSize <= 0 {
		return fmt.Errorf("config: max-datagram-size must be > 0, got %d", c.MaxDatagramSize)
	}
	if c.IdleTimeout <= 0 {
		return fmt.Errorf("config: idle-timeout must be > 0, got %v", c.IdleTimeout)
	}
	switch c.Lang {
	case "", "en", "zh-CN", "zh-TW":
	default:
		return fmt.Errorf("config: invalid lang %q (en|zh-CN|zh-TW, empty=auto)", c.Lang)
	}
	for _, cidr := range c.TrustedNetworks {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("config: invalid trusted-networks entry %q: %w", cidr, err)
		}
	}
	if c.DetectTimeout <= 0 {
		return fmt.Errorf("config: detect-timeout must be > 0, got %v", c.DetectTimeout)
	}
	if !validLevel(c.LogConsoleLevel) {
		return fmt.Errorf("config: invalid log console level %q (debug|info|warn|error)", c.LogConsoleLevel)
	}
	if !validFormat(c.LogConsoleFormat) {
		return fmt.Errorf("config: invalid log console format %q (text|json)", c.LogConsoleFormat)
	}
	if c.LogFilePath != "" {
		if !validLevel(c.LogFileLevel) {
			return fmt.Errorf("config: invalid log file level %q (debug|info|warn|error)", c.LogFileLevel)
		}
		if !validFormat(c.LogFileFormat) {
			return fmt.Errorf("config: invalid log file format %q (text|json)", c.LogFileFormat)
		}
	}
	return nil
}

func validLevel(s string) bool {
	switch s {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

func validFormat(s string) bool {
	switch s {
	case "text", "json":
		return true
	}
	return false
}

// parseCIDRList splits a comma-separated CIDR string, trims whitespace
// from each entry, and skips empty strings. Used by envSource and flagSource
// for the -trusted-networks value.
func parseCIDRList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveConfigPath returns the config file path and whether it is optional.
// An explicit -config is required (missing file is an error); the default
// exe-dir path is optional (missing file is silently skipped).
func resolveConfigPath(set map[string]bool, configFlag, defaultPath string) (path string, optional bool) {
	if set["config"] && configFlag != "" {
		return configFlag, false
	}
	return defaultPath, true
}

// DefaultConfigPath is the auto-discovered location: config.yaml next to the
// running executable. Exposed so the `init` subcommand can write a sample there.
func DefaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "config.yaml")
}

// WriteSample writes a commented config.yaml template to path (creating parent
// directories). The template has upstream empty on purpose so a user must fill
// it in before the config validates.
func WriteSample(path string) error {
	if path == "" {
		return errors.New("config: cannot write sample: empty path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", dir, err)
		}
	}
	return os.WriteFile(path, []byte(sampleConfig), 0o644)
}

// sampleConfig is the -init template. It must round-trip through the file
// source (i.e. parse as valid YAML matching yamlFields).
const sampleConfig = `# ProxyDge configuration file.
# Generated by: proxydge -init
#
# Precedence (highest to lowest): CLI flags > env (PROXYDGE_*) > this file > defaults.

version: 2

listen: ":9000"          # listen address (host:port)
upstream: ""             # REQUIRED: downstream target host:port, e.g. 127.0.0.1:9001
policy: "use"            # use | require | reject
detect-timeout: "1s"     # PROXY header detection timeout
lang: ""                  # display language: en|zh-CN|zh-TW (empty=auto)

# Trust control: only these networks may send PROXY headers.
# Empty (default) trusts everyone — configure in production to prevent spoofing.
trusted-networks:
  # - "10.0.0.0/8"
  # - "192.168.1.0/24"
untrusted-proxy-action: "reject"   # reject (default) | strip

# UDP gateway mode (protocol=udp). Ignored when protocol=tcp.
protocol: "tcp"           # tcp (default) | udp
idle-timeout: "30s"       # UDP session idle timeout
max-sessions: 1024        # max concurrent UDP sessions
max-datagram-size: 65535  # max datagram size, oversized=drop
udp-output: "every_datagram"  # every_datagram (default) | first_datagram

log:
  console:              # logs to stderr
    level: "info"        # debug | info | warn | error
    format: "text"       # text | json
  file:                 # logs to a file (path empty => disabled); v1: no rotation
    path: ""             # e.g. /var/log/proxydge.log
    level: "info"
    format: "json"
`

// --- defaults source ---

type defaultsSource struct{}

func (defaultsSource) Apply(c *Config) error {
	c.Listen = ":9000"
	c.Policy = "use"
	c.DetectTimeout = time.Second
	c.LogConsoleLevel = "info"
	c.LogConsoleFormat = "text"
	c.LogFileLevel = "info"
	c.LogFileFormat = "text"
	c.UntrustedProxyAction = "reject"
	c.Protocol = "tcp"
	c.IdleTimeout = 30 * time.Second
	c.MaxSessions = 1024
	c.MaxDatagramSize = 65535
	c.UDPOutput = "every_datagram"
	// LogFilePath defaults to "" (file sink off).
	// TrustedNetworks defaults to nil (trust everyone).
	// Every field originates from defaults; higher-precedence sources overwrite.
	for _, f := range configFields {
		c.mark(f.name, "default")
	}
	return nil
}

// --- file source (YAML) ---

type fileSource struct {
	path     string
	optional bool // true => missing file is silently skipped
}

// yamlFields uses pointers so we can tell "present in file" from "absent",
// letting the file source overlay only the keys the user actually wrote. The
// log section is nested; pointers on the inner structs preserve presence.
type yamlFields struct {
	Version              *int     `yaml:"version"`
	Listen               *string  `yaml:"listen"`
	Upstream             *string  `yaml:"upstream"`
	Policy               *string  `yaml:"policy"`
	DetectTimeout        *string  `yaml:"detect-timeout"`
	Lang                 *string  `yaml:"lang"`
	Log                  *yamlLog `yaml:"log"`
	TrustedNetworks      []string `yaml:"trusted-networks"`
	UntrustedProxyAction *string  `yaml:"untrusted-proxy-action"`
	Protocol             *string  `yaml:"protocol"`
	IdleTimeout          *string  `yaml:"idle-timeout"`
	MaxSessions          *int     `yaml:"max-sessions"`
	MaxDatagramSize      *int     `yaml:"max-datagram-size"`
	UDPOutput            *string  `yaml:"udp-output"`
}

type yamlLog struct {
	Console *yamlLogConsole `yaml:"console"`
	File    *yamlLogFile    `yaml:"file"`
}

type yamlLogConsole struct {
	Level  *string `yaml:"level"`
	Format *string `yaml:"format"`
}

type yamlLogFile struct {
	Path   *string `yaml:"path"`
	Level  *string `yaml:"level"`
	Format *string `yaml:"format"`
}

func (s fileSource) Apply(c *Config) error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if s.optional && errors.Is(err, os.ErrNotExist) {
			return nil // auto-discovered default absent: not an error
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var y yamlFields
	if err := yaml.Unmarshal(data, &y); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}

	// Version check + auto-migration.
	if y.Version == nil {
		return fmt.Errorf("parse %s: missing 'version' field, add 'version: %d' or run 'proxydge init'", s.path, currentConfigVersion)
	}
	if *y.Version > currentConfigVersion {
		return fmt.Errorf("parse %s: config version %d is newer than supported version %d, upgrade proxydge", s.path, *y.Version, currentConfigVersion)
	}
	if *y.Version < currentConfigVersion {
		// Parse raw map to preserve unknown fields during migration.
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", s.path, err)
		}
		// Backup must succeed before writing the new file.
		bakPath := s.path + ".bak"
		if err := os.WriteFile(bakPath, data, 0o644); err != nil {
			return fmt.Errorf("migrate %s: backup failed: %w", s.path, err)
		}
		migrated := generateMigratedConfig(&y, raw)
		if err := os.WriteFile(s.path, []byte(migrated), 0o644); err != nil {
			return fmt.Errorf("migrate %s: write failed: %w", s.path, err)
		}
		c.Migrated = true
	}

	// A file was actually loaded — record it for the startup banner.
	c.loadedFile = s.path
	src := "file"
	if y.Listen != nil {
		c.Listen = *y.Listen
		c.mark(fListen, src)
	}
	if y.Upstream != nil {
		c.Upstream = *y.Upstream
		c.mark(fUpstream, src)
	}
	if y.Policy != nil {
		c.Policy = *y.Policy
		c.mark(fPolicy, src)
	}
	if y.DetectTimeout != nil {
		d, derr := time.ParseDuration(*y.DetectTimeout)
		if derr != nil {
			return fmt.Errorf("parse %s: detect-timeout %q: %w", s.path, *y.DetectTimeout, derr)
		}
		c.DetectTimeout = d
		c.mark(fDetectTimeout, src)
	}
	if y.Lang != nil {
		c.Lang = *y.Lang
		c.mark(fLang, src)
	}
	if y.TrustedNetworks != nil {
		c.TrustedNetworks = y.TrustedNetworks
		c.mark(fTrustedNetworks, src)
	}
	if y.UntrustedProxyAction != nil {
		c.UntrustedProxyAction = *y.UntrustedProxyAction
		c.mark(fUntrustedProxyAction, src)
	}
	if y.Protocol != nil {
		c.Protocol = *y.Protocol
		c.mark(fProtocol, src)
	}
	if y.IdleTimeout != nil {
		d, derr := time.ParseDuration(*y.IdleTimeout)
		if derr != nil {
			return fmt.Errorf("parse %s: idle-timeout %q: %w", s.path, *y.IdleTimeout, derr)
		}
		c.IdleTimeout = d
		c.mark(fIdleTimeout, src)
	}
	if y.MaxSessions != nil {
		c.MaxSessions = *y.MaxSessions
		c.mark(fMaxSessions, src)
	}
	if y.MaxDatagramSize != nil {
		c.MaxDatagramSize = *y.MaxDatagramSize
		c.mark(fMaxDatagramSize, src)
	}
	if y.UDPOutput != nil {
		c.UDPOutput = *y.UDPOutput
		c.mark(fUDPOutput, src)
	}
	if y.Log != nil {
		if y.Log.Console != nil {
			if y.Log.Console.Level != nil {
				c.LogConsoleLevel = *y.Log.Console.Level
				c.mark(fLogConsoleLevel, src)
			}
			if y.Log.Console.Format != nil {
				c.LogConsoleFormat = *y.Log.Console.Format
				c.mark(fLogConsoleFormat, src)
			}
		}
		if y.Log.File != nil {
			if y.Log.File.Path != nil {
				c.LogFilePath = *y.Log.File.Path
				c.mark(fLogFilePath, src)
			}
			if y.Log.File.Level != nil {
				c.LogFileLevel = *y.Log.File.Level
				c.mark(fLogFileLevel, src)
			}
			if y.Log.File.Format != nil {
				c.LogFileFormat = *y.Log.File.Format
				c.mark(fLogFileFormat, src)
			}
		}
	}
	return nil
}

// --- config migration (auto-upgrade old config files) ---

// knownConfigKeys is the set of top-level keys ProxyDge understands. Keys
// not in this set are preserved verbatim during migration.
var knownConfigKeys = map[string]bool{
	"version":                true,
	"listen":                 true,
	"upstream":               true,
	"policy":                 true,
	"detect-timeout":         true,
	"lang":                   true,
	"trusted-networks":       true,
	"untrusted-proxy-action": true,
	"protocol":               true,
	"idle-timeout":           true,
	"max-sessions":           true,
	"max-datagram-size":      true,
	"udp-output":             true,
	"log":                    true,
}

// generateMigratedConfig builds a new config.yaml string from the parsed
// user fields (preserving their values) + defaults for missing fields +
// comments. Unknown top-level keys from raw are appended verbatim so the
// migration never silently drops user content.
func generateMigratedConfig(y *yamlFields, raw map[string]any) string {
	var b strings.Builder
	b.WriteString("# ProxyDge configuration file.\n")
	b.WriteString("# Auto-migrated by proxydge.\n")
	b.WriteString("# Precedence (highest to lowest): CLI flags > env (PROXYDGE_*) > this file > defaults.\n\n")
	fmt.Fprintf(&b, "version: %d\n\n", currentConfigVersion)

	writeStrField(&b, "listen", y.Listen, ":9000", "listen address (host:port)")
	writeStrField(&b, "upstream", y.Upstream, "", "REQUIRED: downstream target host:port, e.g. 127.0.0.1:9001")
	writeStrField(&b, "policy", y.Policy, "use", "use | require | reject")
	writeStrField(&b, "detect-timeout", y.DetectTimeout, "1s", "PROXY header detection timeout")
	writeStrField(&b, "lang", y.Lang, "", "display language: en|zh-CN (empty=auto)")

	b.WriteString("\n# Trust control: only these networks may send PROXY headers.\n")
	b.WriteString("# Empty (default) trusts everyone — configure in production to prevent spoofing.\n")
	if len(y.TrustedNetworks) > 0 {
		b.WriteString("trusted-networks:\n")
		for _, cidr := range y.TrustedNetworks {
			fmt.Fprintf(&b, "  - %q\n", cidr)
		}
	} else {
		b.WriteString("trusted-networks:\n  # - \"10.0.0.0/8\"\n  # - \"192.168.1.0/24\"\n")
	}
	writeStrField(&b, "untrusted-proxy-action", y.UntrustedProxyAction, "reject", "reject (default) | strip")

	b.WriteString("\n# UDP gateway mode (protocol=udp). Ignored when protocol=tcp.\n")
	writeStrField(&b, "protocol", y.Protocol, "tcp", "tcp (default) | udp")
	writeStrField(&b, "idle-timeout", y.IdleTimeout, "30s", "UDP session idle timeout")
	maxSessions := 1024
	if y.MaxSessions != nil {
		maxSessions = *y.MaxSessions
	}
	fmt.Fprintf(&b, "max-sessions: %d  # max concurrent UDP sessions\n", maxSessions)
	maxDatagramSize := 65535
	if y.MaxDatagramSize != nil {
		maxDatagramSize = *y.MaxDatagramSize
	}
	fmt.Fprintf(&b, "max-datagram-size: %d  # max datagram size, oversized=drop\n", maxDatagramSize)
	writeStrField(&b, "udp-output", y.UDPOutput, "every_datagram", "every_datagram (default) | first_datagram")

	b.WriteString("\nlog:\n")
	b.WriteString("  console:              # logs to stderr\n")
	var cl, cf, fl, ff, fp *string
	if y.Log != nil && y.Log.Console != nil {
		cl, cf = y.Log.Console.Level, y.Log.Console.Format
	}
	if y.Log != nil && y.Log.File != nil {
		fp, fl, ff = y.Log.File.Path, y.Log.File.Level, y.Log.File.Format
	}
	writeStrFieldIndent(&b, "level", cl, "info", "debug | info | warn | error", "    ")
	writeStrFieldIndent(&b, "format", cf, "text", "text | json", "    ")
	b.WriteString("  file:                 # logs to a file (path empty => disabled); v1: no rotation\n")
	writeStrFieldIndent(&b, "path", fp, "", "e.g. /var/log/proxydge.log", "    ")
	writeStrFieldIndent(&b, "level", fl, "info", "debug | info | warn | error", "    ")
	writeStrFieldIndent(&b, "format", ff, "json", "text | json", "    ")

	// Preserve unknown top-level fields verbatim.
	for k, v := range raw {
		if !knownConfigKeys[k] {
			b.WriteString("\n")
			data, _ := yaml.Marshal(map[string]any{k: v})
			b.Write(data)
		}
	}
	return b.String()
}

func writeStrField(b *strings.Builder, name string, val *string, def string, comment string) {
	v := def
	if val != nil {
		v = *val
	}
	fmt.Fprintf(b, "%s: %q  # %s\n", name, v, comment)
}

func writeStrFieldIndent(b *strings.Builder, name string, val *string, def string, comment string, indent string) {
	v := def
	if val != nil {
		v = *val
	}
	fmt.Fprintf(b, "%s%s: %q  # %s\n", indent, name, v, comment)
}

// --- env source ---

const envPrefix = "PROXYDGE_"

type envSource struct{}

func (envSource) Apply(c *Config) error {
	if v, ok := os.LookupEnv(envPrefix + "LISTEN"); ok && v != "" {
		c.Listen = v
		c.mark(fListen, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UPSTREAM"); ok && v != "" {
		c.Upstream = v
		c.mark(fUpstream, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "POLICY"); ok && v != "" {
		c.Policy = v
		c.mark(fPolicy, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "DETECT_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%sDETECT_TIMEOUT=%q: %w", envPrefix, v, err)
		}
		c.DetectTimeout = d
		c.mark(fDetectTimeout, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "LANG"); ok && v != "" {
		c.Lang = v
		c.mark(fLang, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "LOG_CONSOLE_LEVEL"); ok && v != "" {
		c.LogConsoleLevel = v
		c.mark(fLogConsoleLevel, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "LOG_CONSOLE_FORMAT"); ok && v != "" {
		c.LogConsoleFormat = v
		c.mark(fLogConsoleFormat, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "LOG_FILE"); ok && v != "" {
		c.LogFilePath = v
		c.mark(fLogFilePath, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "LOG_FILE_LEVEL"); ok && v != "" {
		c.LogFileLevel = v
		c.mark(fLogFileLevel, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "LOG_FILE_FORMAT"); ok && v != "" {
		c.LogFileFormat = v
		c.mark(fLogFileFormat, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "TRUSTED_NETWORKS"); ok && v != "" {
		c.TrustedNetworks = parseCIDRList(v)
		c.mark(fTrustedNetworks, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UNTRUSTED_PROXY_ACTION"); ok && v != "" {
		c.UntrustedProxyAction = v
		c.mark(fUntrustedProxyAction, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "PROTOCOL"); ok && v != "" {
		c.Protocol = v
		c.mark(fProtocol, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "IDLE_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%sIDLE_TIMEOUT=%q: %w", envPrefix, v, err)
		}
		c.IdleTimeout = d
		c.mark(fIdleTimeout, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "MAX_SESSIONS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%sMAX_SESSIONS=%q: %w", envPrefix, v, err)
		}
		c.MaxSessions = n
		c.mark(fMaxSessions, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "MAX_DATAGRAM_SIZE"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%sMAX_DATAGRAM_SIZE=%q: %w", envPrefix, v, err)
		}
		c.MaxDatagramSize = n
		c.mark(fMaxDatagramSize, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UDP_OUTPUT"); ok && v != "" {
		c.UDPOutput = v
		c.mark(fUDPOutput, "env")
	}
	return nil
}

// --- flag source ---

// flagValues are the parsed flag pointers (zero defaults: the flag source only
// applies flags that were explicitly set).
type flagValues struct {
	listen, upstream, policy, config         *string
	detectTimeout                            *time.Duration
	lang                                     *string
	logConsoleLevel, logConsoleFormat        *string
	logFilePath, logFileLevel, logFileFormat *string
	trustedNetworks                          *string
	untrustedProxyAction                     *string
	protocol                                 *string
	idleTimeout                              *time.Duration
	maxSessions                              *int
	maxDatagramSize                          *int
	udpOutput                                *string
}

func parseFlags(args []string) (*flagValues, map[string]bool, error) {
	fv := &flagValues{}
	fs := flag.NewFlagSet("proxydge start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fv.listen = fs.String("listen", "", "listen address (host:port)")
	fv.upstream = fs.String("upstream", "", "downstream target host:port (required)")
	fv.policy = fs.String("policy", "", "upstream header policy: use|require|reject")
	fv.config = fs.String("config", "", "config file path (overrides exe-dir config.yaml)")
	fv.detectTimeout = fs.Duration("detect-timeout", 0, "PROXY header detection timeout")
	fv.lang = fs.String("lang", "", "display language: en|zh-CN|zh-TW (default auto)")
	fv.logConsoleLevel = fs.String("log-console-level", "", "console log level: debug|info|warn|error")
	fv.logConsoleFormat = fs.String("log-console-format", "", "console log format: text|json")
	fv.logFilePath = fs.String("log-file", "", "file log path (empty=disabled)")
	fv.logFileLevel = fs.String("log-file-level", "", "file log level: debug|info|warn|error")
	fv.logFileFormat = fs.String("log-file-format", "", "file log format: text|json")
	fv.trustedNetworks = fs.String("trusted-networks", "", "trusted networks (comma-separated CIDRs, empty=all)")
	fv.untrustedProxyAction = fs.String("untrusted-proxy-action", "", "action for untrusted sources with PROXY header: reject|strip")
	fv.protocol = fs.String("protocol", "", "transport protocol: tcp|udp")
	fv.idleTimeout = fs.Duration("idle-timeout", 0, "UDP session idle timeout")
	fv.maxSessions = fs.Int("max-sessions", 0, "max concurrent UDP sessions")
	fv.maxDatagramSize = fs.Int("max-datagram-size", 0, "max datagram size, oversized=drop")
	fv.udpOutput = fs.String("udp-output", "", "UDP output mode: every_datagram|first_datagram")
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return fv, set, nil
}

type flagSource struct {
	fv  *flagValues
	set map[string]bool
}

func (s flagSource) Apply(c *Config) error {
	if s.set["listen"] {
		c.Listen = *s.fv.listen
		c.mark(fListen, "flag")
	}
	if s.set["upstream"] {
		c.Upstream = *s.fv.upstream
		c.mark(fUpstream, "flag")
	}
	if s.set["policy"] {
		c.Policy = *s.fv.policy
		c.mark(fPolicy, "flag")
	}
	if s.set["detect-timeout"] {
		c.DetectTimeout = *s.fv.detectTimeout
		c.mark(fDetectTimeout, "flag")
	}
	if s.set["lang"] {
		c.Lang = *s.fv.lang
		c.mark(fLang, "flag")
	}
	if s.set["config"] {
		c.ConfigPath = *s.fv.config
	}
	if s.set["log-console-level"] {
		c.LogConsoleLevel = *s.fv.logConsoleLevel
		c.mark(fLogConsoleLevel, "flag")
	}
	if s.set["log-console-format"] {
		c.LogConsoleFormat = *s.fv.logConsoleFormat
		c.mark(fLogConsoleFormat, "flag")
	}
	if s.set["log-file"] {
		c.LogFilePath = *s.fv.logFilePath
		c.mark(fLogFilePath, "flag")
	}
	if s.set["log-file-level"] {
		c.LogFileLevel = *s.fv.logFileLevel
		c.mark(fLogFileLevel, "flag")
	}
	if s.set["log-file-format"] {
		c.LogFileFormat = *s.fv.logFileFormat
		c.mark(fLogFileFormat, "flag")
	}
	if s.set["trusted-networks"] {
		c.TrustedNetworks = parseCIDRList(*s.fv.trustedNetworks)
		c.mark(fTrustedNetworks, "flag")
	}
	if s.set["untrusted-proxy-action"] {
		c.UntrustedProxyAction = *s.fv.untrustedProxyAction
		c.mark(fUntrustedProxyAction, "flag")
	}
	if s.set["protocol"] {
		c.Protocol = *s.fv.protocol
		c.mark(fProtocol, "flag")
	}
	if s.set["idle-timeout"] {
		c.IdleTimeout = *s.fv.idleTimeout
		c.mark(fIdleTimeout, "flag")
	}
	if s.set["max-sessions"] {
		c.MaxSessions = *s.fv.maxSessions
		c.mark(fMaxSessions, "flag")
	}
	if s.set["max-datagram-size"] {
		c.MaxDatagramSize = *s.fv.maxDatagramSize
		c.mark(fMaxDatagramSize, "flag")
	}
	if s.set["udp-output"] {
		c.UDPOutput = *s.fv.udpOutput
		c.mark(fUDPOutput, "flag")
	}
	return nil
}
