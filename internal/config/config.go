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
	TCPIdleTimeout       time.Duration // TCP pipe idle timeout (0=disabled, default 5m)
	TCPHeaderVersion     string        // downstream PROXY header version: "v1" | "v2" (default "v2")
	TCPFamilyMismatch    string        // mixed address-family action: "reject" | "unknown" | "legacy" (default "reject")
	TCPMaxConnections    int           // max concurrent TCP connections (0=unlimited, default 4096)
	Lang                 string        // display language: "" (auto) | "en" | "zh-CN"
	TrustedNetworks      []string
	UntrustedProxyAction string
	Protocol             string        // "tcp" (default) | "udp"
	IdleTimeout          time.Duration // UDP session idle timeout (default 30s)
	MaxSessions          int           // max concurrent UDP sessions (0=unlimited, default 1024)
	MaxDatagramSize      int           // max datagram size, oversized=drop (default 65535)
	UDPHeaderMode        string        // "every_datagram" (default) | "first_datagram"
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
//
// History:
//   - 2 → 3 (this change): introduces tcp.header-version and tcp.family-mismatch.
//     The bump exists so pre-v3 files migrate and get an EXPLICIT
//     family-mismatch value: migration injects "legacy" when the key is absent,
//     preserving historical wire behavior byte-for-byte for upgraded deployments.
//     Fresh configs (and -init) default to "reject". The same migration pass
//     also back-fills tcp.idle-timeout (introduced in v0.3.2 without a version
//     bump, so older version-2 files never gained the line).
const currentConfigVersion = 3

// ConfigError carries an i18n message key so main.go can translate it.
// The msg field is an English fallback for Go's error chain (logging, %w wrapping).
// Non-ConfigError errors (e.g. from file I/O or yaml parsing) fall back to English.
type ConfigError struct {
	Key  string
	Args []any
	msg  string
}

func (e *ConfigError) Error() string { return e.msg }

// cfgErr creates a ConfigError with an i18n key and English fallback.
// args are shared between the English format and the i18n template.
func cfgErr(key, format string, args ...any) *ConfigError {
	return &ConfigError{Key: key, Args: args, msg: fmt.Sprintf(format, args...)}
}

// mark records that source provided field. Sources call this as they overlay,
// so later (higher-precedence) sources overwrite earlier ones — prov[field]
// ends up being the winning source.
func (c *Config) mark(field, source string) {
	if c.prov == nil {
		c.prov = map[string]string{}
	}
	c.prov[field] = source
}

// configField is a single entry in the config field registry — the central
// metadata table that drives Describe output, default-value verification, and
// template consistency checks. Every user-configurable field has exactly one
// entry here; adding a field without registering it causes TestFieldRegistry
// to fail in CI.
type configField struct {
	name    string // v2 display name: "tcp.detect-timeout"
	section string // "General", "Trust", "TCP", "UDP", "Logging"
	defVal  any    // default value (for consistency test vs defaultsSource)
	comment string // inline comment (for documentation)
	value   func(*Config) any
}

// configFields is the config field registry — the single source of truth for
// field display names, section grouping, default values, and display order.
// Describe() iterates this table; defaultsSource is verified against it by
// TestFieldRegistryConsistency; sampleConfig and generateMigratedConfig must
// contain every field name listed here.
var configFields = []configField{
	// ── General
	{"protocol", "General", "tcp", "tcp (default) | udp — selects gateway mode", func(c *Config) any { return c.Protocol }},
	{"listen", "General", ":9000", "listen address (host:port)", func(c *Config) any { return c.Listen }},
	{"upstream", "General", "127.0.0.1:9001", "downstream target host:port", func(c *Config) any { return c.Upstream }},
	{"policy", "General", "use", "use | require | reject", func(c *Config) any { return c.Policy }},
	{"lang", "General", "", "display language: en|zh-CN|zh-TW (empty=auto)", func(c *Config) any { return c.Lang }},
	// ── Trust
	{"trusted-networks", "Trust", []string(nil), "only these networks may send PROXY headers", func(c *Config) any { return c.TrustedNetworks }},
	{"untrusted-proxy-action", "Trust", "reject", "reject (default) | strip", func(c *Config) any { return c.UntrustedProxyAction }},
	// ── TCP
	{"tcp.detect-timeout", "TCP", time.Second, "PROXY header detection timeout (0=block indefinitely)", func(c *Config) any { return c.DetectTimeout }},
	{"tcp.idle-timeout", "TCP", 5 * time.Minute, "pipe idle timeout, 0=disabled", func(c *Config) any { return c.TCPIdleTimeout }},
	{"tcp.header-version", "TCP", "v2", "downstream PROXY header version: v1|v2", func(c *Config) any { return c.TCPHeaderVersion }},
	{"tcp.family-mismatch", "TCP", "reject", "mixed address-family action: reject|unknown|legacy", func(c *Config) any { return c.TCPFamilyMismatch }},
	{"tcp.max-connections", "TCP", 4096, "max concurrent connections, 0=unlimited, over-limit accept closed", func(c *Config) any { return c.TCPMaxConnections }},
	// ── UDP
	{"udp.idle-timeout", "UDP", 30 * time.Second, "UDP session idle timeout", func(c *Config) any { return c.IdleTimeout }},
	{"udp.max-sessions", "UDP", 1024, "max concurrent UDP sessions, 0=unlimited", func(c *Config) any { return c.MaxSessions }},
	{"udp.max-datagram-size", "UDP", 65535, "max datagram size, 0=unlimited, oversized=drop", func(c *Config) any { return c.MaxDatagramSize }},
	{"udp.header-mode", "UDP", "every_datagram", "every_datagram (default) | first_datagram", func(c *Config) any { return c.UDPHeaderMode }},
	// ── Logging
	{"log.console.level", "Logging", "info", "debug | info | warn | error", func(c *Config) any { return c.LogConsoleLevel }},
	{"log.console.format", "Logging", "text", "text | json", func(c *Config) any { return c.LogConsoleFormat }},
	{"log.file.path", "Logging", "", "e.g. /var/log/proxydge.log", func(c *Config) any { return c.LogFilePath }},
	{"log.file.level", "Logging", "info", "debug | info | warn | error", func(c *Config) any { return c.LogFileLevel }},
	{"log.file.format", "Logging", "text", "text | json", func(c *Config) any { return c.LogFileFormat }},
}

// fieldName constants keep the Sources' mark() calls aligned with the
// registry. Each constant must match the corresponding configFields .name.
const (
	fProtocol             = "protocol"
	fListen               = "listen"
	fUpstream             = "upstream"
	fPolicy               = "policy"
	fLang                 = "lang"
	fTrustedNetworks      = "trusted-networks"
	fUntrustedProxyAction = "untrusted-proxy-action"
	fDetectTimeout        = "tcp.detect-timeout"
	fTCPIdleTimeout       = "tcp.idle-timeout"
	fTCPHeaderVersion     = "tcp.header-version"
	fTCPFamilyMismatch    = "tcp.family-mismatch"
	fTCPMaxConnections    = "tcp.max-connections"
	fIdleTimeout          = "udp.idle-timeout"
	fMaxSessions          = "udp.max-sessions"
	fMaxDatagramSize      = "udp.max-datagram-size"
	fUDPHeaderMode        = "udp.header-mode"
	fLogConsoleLevel      = "log.console.level"
	fLogConsoleFormat     = "log.console.format"
	fLogFilePath          = "log.file.path"
	fLogFileLevel         = "log.file.level"
	fLogFileFormat        = "log.file.format"
)

// Describe returns a human-readable dump of every config field with its value
// and the source that provided it, plus the config file that was loaded (if
// any). Intended for the startup banner. Fields are grouped by section
// (General, Trust, TCP, UDP, Logging) matching the config file structure.
func (c *Config) Describe() string {
	var b strings.Builder
	b.WriteString("config file: ")
	if c.loadedFile != "" {
		b.WriteString(c.loadedFile)
	} else {
		b.WriteString("(none)")
	}
	b.WriteString("\n\n-- config --\n")
	var lastSection string
	for _, f := range configFields {
		if f.section != lastSection {
			fmt.Fprintf(&b, "  [%s]\n", f.section)
			lastSection = f.section
		}
		fmt.Fprintf(&b, "    %s = %v (%s)\n", f.name, f.value(c), c.sourceOf(f.name))
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
	// legacy keeps the historical auto-conversion for mixed address-family
	// headers (::ffff:-mapped IPv4 may reach downstream labeled as IPv6).
	// Surface it at startup so operators opt in knowingly, not by omission.
	if c.Protocol == "tcp" && c.TCPFamilyMismatch == "legacy" {
		ws = append(ws, Warning{Key: "warning.family_mismatch.legacy"})
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
		return cfgErr("error.upstream_required", "config: upstream is required")
	}
	switch c.Policy {
	case "use", "require", "reject":
	default:
		return cfgErr("error.invalid_policy", "config: invalid policy %q (use|require|reject)", c.Policy)
	}
	switch c.UntrustedProxyAction {
	case "reject", "strip":
	default:
		return cfgErr("error.invalid_untrusted_proxy_action", "config: invalid untrusted-proxy-action %q (reject|strip)", c.UntrustedProxyAction)
	}
	switch c.Protocol {
	case "tcp", "udp":
	default:
		return cfgErr("error.invalid_protocol", "config: invalid protocol %q (tcp|udp)", c.Protocol)
	}
	switch c.UDPHeaderMode {
	case "every_datagram", "first_datagram":
	default:
		return cfgErr("error.invalid_udp_header_mode", "config: invalid udp.header-mode %q (every_datagram|first_datagram)", c.UDPHeaderMode)
	}
	if c.MaxSessions < 0 {
		return cfgErr("error.max_sessions_nonneg", "config: max-sessions must be >= 0 (0=unlimited), got %d", c.MaxSessions)
	}
	if c.MaxDatagramSize < 0 {
		return cfgErr("error.max_datagram_size_nonneg", "config: max-datagram-size must be >= 0 (0=unlimited), got %d", c.MaxDatagramSize)
	}
	if c.IdleTimeout <= 0 {
		return cfgErr("error.idle_timeout_positive", "config: idle-timeout must be > 0, got %v", c.IdleTimeout)
	}
	switch c.Lang {
	case "", "en", "zh-CN", "zh-TW":
	default:
		return cfgErr("error.invalid_lang", "config: invalid lang %q (en|zh-CN|zh-TW, empty=auto)", c.Lang)
	}
	for _, cidr := range c.TrustedNetworks {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			if net.ParseIP(cidr) == nil {
				return cfgErr("error.invalid_trusted_networks_entry", "config: invalid trusted-networks entry %q: not a valid CIDR or IP address", cidr)
			}
		}
	}
	if c.DetectTimeout < 0 {
		return cfgErr("error.detect_timeout_nonneg", "config: detect-timeout must be >= 0 (0=block indefinitely), got %v", c.DetectTimeout)
	}
	if c.TCPIdleTimeout < 0 {
		return cfgErr("error.tcp_idle_timeout_nonneg", "config: tcp.idle-timeout must be >= 0 (0=disabled), got %v", c.TCPIdleTimeout)
	}
	switch c.TCPHeaderVersion {
	case "v1", "v2":
	default:
		return cfgErr("error.invalid_tcp_header_version", "config: invalid tcp.header-version %q (v1|v2)", c.TCPHeaderVersion)
	}
	switch c.TCPFamilyMismatch {
	case "reject", "unknown", "legacy":
	default:
		return cfgErr("error.invalid_family_mismatch", "config: invalid tcp.family-mismatch %q (reject|unknown|legacy)", c.TCPFamilyMismatch)
	}
	if c.TCPMaxConnections < 0 {
		return cfgErr("error.max_connections_nonneg", "config: tcp.max-connections must be >= 0 (0=unlimited), got %d", c.TCPMaxConnections)
	}
	if !validLevel(c.LogConsoleLevel) {
		return cfgErr("error.invalid_log_console_level", "config: invalid log console level %q (debug|info|warn|error)", c.LogConsoleLevel)
	}
	if !validFormat(c.LogConsoleFormat) {
		return cfgErr("error.invalid_log_console_format", "config: invalid log console format %q (text|json)", c.LogConsoleFormat)
	}
	if c.LogFilePath != "" {
		if !validLevel(c.LogFileLevel) {
			return cfgErr("error.invalid_log_file_level", "config: invalid log file level %q (debug|info|warn|error)", c.LogFileLevel)
		}
		if !validFormat(c.LogFileFormat) {
			return cfgErr("error.invalid_log_file_format", "config: invalid log file format %q (text|json)", c.LogFileFormat)
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
// directories). The template uses the default upstream (127.0.0.1:9001) so it
// validates out of the box; operators can override via flag/env/edited file.
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

version: 3  # config format version — do NOT change; used for auto-migration

# ── General ───────────────────────────────────────────────────────────
protocol: "tcp"                      # tcp (default) | udp — selects gateway mode
listen: ":9000"                      # listen address (host:port)
upstream: "127.0.0.1:9001"          # downstream target host:port
policy: "use"                        # use | require | reject
lang: ""                             # display language: en|zh-CN|zh-TW (empty=auto)

# Trust control: only these networks may send PROXY headers.
# Supports CIDR (10.0.0.0/8, 2001:db8::/32) and bare IPs (10.0.0.1, fe80::1).
# Empty (default) trusts everyone — configure in production to prevent spoofing.
trusted-networks:
  # - "10.0.0.0/8"
  # - "192.168.1.0/24"
  # - "2001:db8::/32"
  # - "10.0.0.1"        # bare IP → /32 (IPv4) or /128 (IPv6)
untrusted-proxy-action: "reject"     # reject (default) | strip

# ── TCP (protocol=tcp) ───────────────────────────────────────────────
tcp:
  detect-timeout: "1s"               # PROXY header detection timeout (0=block indefinitely)
  idle-timeout: "5m"                 # pipe idle timeout, 0=disabled
  header-version: "v2"               # downstream PROXY header version: v1|v2
  family-mismatch: "reject"          # mixed address-family action: reject|unknown|legacy
  max-connections: 4096              # max concurrent connections, 0=unlimited

# ── UDP (protocol=udp) ───────────────────────────────────────────────
# The following fields are only used when protocol=udp.
udp:
  idle-timeout: "30s"               # UDP session idle timeout
  max-sessions: 1024                # max concurrent UDP sessions, 0=unlimited
  max-datagram-size: 65535          # max datagram size, 0=unlimited, oversized=drop
  header-mode: every_datagram       # every_datagram (default) | first_datagram

# ── Logging ──────────────────────────────────────────────────────────
log:
  console:                          # logs to stderr
    level: "info"                    # debug | info | warn | error
    format: "text"                   # text | json
  file:                             # logs to a file (path empty => disabled)
    path: ""                         # e.g. /var/log/proxydge.log
    level: "info"
    format: "text"
`

// --- defaults source ---

type defaultsSource struct{}

func (defaultsSource) Apply(c *Config) error {
	c.Upstream = "127.0.0.1:9001"
	c.Listen = ":9000"
	c.Policy = "use"
	c.DetectTimeout = time.Second
	c.TCPIdleTimeout = 5 * time.Minute
	c.TCPHeaderVersion = "v2"
	c.TCPFamilyMismatch = "reject"
	c.TCPMaxConnections = 4096
	c.LogConsoleLevel = "info"
	c.LogConsoleFormat = "text"
	c.LogFileLevel = "info"
	c.LogFileFormat = "text"
	c.UntrustedProxyAction = "reject"
	c.Protocol = "tcp"
	c.IdleTimeout = 30 * time.Second
	c.MaxSessions = 1024
	c.MaxDatagramSize = 65535
	c.UDPHeaderMode = "every_datagram"
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
	Lang                 *string  `yaml:"lang"`
	TrustedNetworks      []string `yaml:"trusted-networks"`
	UntrustedProxyAction *string  `yaml:"untrusted-proxy-action"`
	Protocol             *string  `yaml:"protocol"`
	TCP                  *yamlTCP `yaml:"tcp"`
	UDP                  *yamlUDP `yaml:"udp"`
	Log                  *yamlLog `yaml:"log"`
}

type yamlTCP struct {
	DetectTimeout  *string `yaml:"detect-timeout"`
	IdleTimeout    *string `yaml:"idle-timeout"`
	HeaderVersion  *string `yaml:"header-version"`
	FamilyMismatch *string `yaml:"family-mismatch"`
	MaxConnections *int    `yaml:"max-connections"`
}

type yamlUDP struct {
	IdleTimeout     *string `yaml:"idle-timeout"`
	MaxSessions     *int    `yaml:"max-sessions"`
	MaxDatagramSize *int    `yaml:"max-datagram-size"`
	HeaderMode      *string `yaml:"header-mode"`
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
		// Re-parse the migrated file so v1→v2 mapped values (e.g. flat
		// detect-timeout → tcp.detect-timeout) are picked up in-memory.
		data, err = os.ReadFile(s.path)
		if err != nil {
			return fmt.Errorf("re-read %s: %w", s.path, err)
		}
		if err := yaml.Unmarshal(data, &y); err != nil {
			return fmt.Errorf("re-parse %s: %w", s.path, err)
		}
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
	if y.TCP != nil && y.TCP.DetectTimeout != nil {
		d, derr := time.ParseDuration(*y.TCP.DetectTimeout)
		if derr != nil {
			return fmt.Errorf("parse %s: detect-timeout %q: %w", s.path, *y.TCP.DetectTimeout, derr)
		}
		c.DetectTimeout = d
		c.mark(fDetectTimeout, src)
	}
	if y.TCP != nil && y.TCP.IdleTimeout != nil {
		d, derr := time.ParseDuration(*y.TCP.IdleTimeout)
		if derr != nil {
			return fmt.Errorf("parse %s: tcp.idle-timeout %q: %w", s.path, *y.TCP.IdleTimeout, derr)
		}
		c.TCPIdleTimeout = d
		c.mark(fTCPIdleTimeout, src)
	}
	if y.TCP != nil && y.TCP.HeaderVersion != nil {
		c.TCPHeaderVersion = *y.TCP.HeaderVersion
		c.mark(fTCPHeaderVersion, src)
	}
	if y.TCP != nil && y.TCP.FamilyMismatch != nil {
		c.TCPFamilyMismatch = *y.TCP.FamilyMismatch
		c.mark(fTCPFamilyMismatch, src)
	}
	if y.TCP != nil && y.TCP.MaxConnections != nil {
		c.TCPMaxConnections = *y.TCP.MaxConnections
		c.mark(fTCPMaxConnections, src)
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
	if y.UDP != nil {
		if y.UDP.IdleTimeout != nil {
			d, derr := time.ParseDuration(*y.UDP.IdleTimeout)
			if derr != nil {
				return fmt.Errorf("parse %s: idle-timeout %q: %w", s.path, *y.UDP.IdleTimeout, derr)
			}
			c.IdleTimeout = d
			c.mark(fIdleTimeout, src)
		}
		if y.UDP.MaxSessions != nil {
			c.MaxSessions = *y.UDP.MaxSessions
			c.mark(fMaxSessions, src)
		}
		if y.UDP.MaxDatagramSize != nil {
			c.MaxDatagramSize = *y.UDP.MaxDatagramSize
			c.mark(fMaxDatagramSize, src)
		}
		if y.UDP.HeaderMode != nil {
			c.UDPHeaderMode = *y.UDP.HeaderMode
			c.mark(fUDPHeaderMode, src)
		}
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
// not in this set are preserved verbatim during migration. v1 flat fields
// (now nested under tcp:/udp:) are included so migration maps them to v2
// locations instead of leaving them as unknown fields at the bottom.
var knownConfigKeys = map[string]bool{
	"version":                true,
	"listen":                 true,
	"upstream":               true,
	"policy":                 true,
	"lang":                   true,
	"trusted-networks":       true,
	"untrusted-proxy-action": true,
	"protocol":               true,
	"tcp":                    true,
	"udp":                    true,
	"log":                    true,
	// v1 flat fields — now nested; recognized so migration maps them.
	"detect-timeout":    true, // → tcp.detect-timeout
	"idle-timeout":      true, // → udp.idle-timeout
	"max-sessions":      true, // → udp.max-sessions
	"max-datagram-size": true, // → udp.max-datagram-size
	"udp-output":        true, // → udp.header-mode (renamed in v2)
	"header-mode":       true, // → udp.header-mode (if written flat)
}

// generateMigratedConfig builds a new config.yaml string from the parsed
// user fields (preserving their values) + defaults for missing fields +
// comments. The output mirrors the -init template (sampleConfig) in field
// order, section headers, and comments. v1 flat fields in raw are mapped
// to their v2 nested locations. Unknown top-level keys from raw are
// appended verbatim so the migration never silently drops user content.
func generateMigratedConfig(y *yamlFields, raw map[string]any) string {
	var b strings.Builder
	b.WriteString("# ProxyDge configuration file.\n")
	b.WriteString("# Auto-migrated by proxydge.\n")
	b.WriteString("# Precedence (highest to lowest): CLI flags > env (PROXYDGE_*) > this file > defaults.\n\n")
	fmt.Fprintf(&b, "version: %d  # config format version — do NOT change; used for auto-migration\n\n", currentConfigVersion)

	// ── General
	b.WriteString("# ── General ───────────────────────────────────────────────────────────\n")
	writeStrField(&b, "protocol", y.Protocol, "tcp", "tcp (default) | udp — selects gateway mode")
	writeStrField(&b, "listen", y.Listen, ":9000", "listen address (host:port)")
	writeStrField(&b, "upstream", y.Upstream, "127.0.0.1:9001", "downstream target host:port")
	writeStrField(&b, "policy", y.Policy, "use", "use | require | reject")
	writeStrField(&b, "lang", y.Lang, "", "display language: en|zh-CN|zh-TW (empty=auto)")

	// ── Trust control
	b.WriteString("\n# Trust control: only these networks may send PROXY headers.\n")
	b.WriteString("# Supports CIDR (10.0.0.0/8, 2001:db8::/32) and bare IPs (10.0.0.1, fe80::1).\n")
	b.WriteString("# Empty (default) trusts everyone — configure in production to prevent spoofing.\n")
	if len(y.TrustedNetworks) > 0 {
		b.WriteString("trusted-networks:\n")
		for _, cidr := range y.TrustedNetworks {
			fmt.Fprintf(&b, "  - %q\n", cidr)
		}
	} else {
		b.WriteString("trusted-networks:\n  # - \"10.0.0.0/8\"\n  # - \"192.168.1.0/24\"\n  # - \"2001:db8::/32\"\n  # - \"10.0.0.1\"        # bare IP → /32 (IPv4) or /128 (IPv6)\n")
	}
	writeStrField(&b, "untrusted-proxy-action", y.UntrustedProxyAction, "reject", "reject (default) | strip")

	// ── TCP
	var detectTimeout *string
	var tcpIdleTimeout *string
	var headerVersion *string
	var familyMismatch *string
	// max-connections: template default 4096; an explicit 0 (unlimited) must
	// survive migration, so the field is copied only when actually present.
	maxConnections := 4096
	if y.TCP != nil {
		detectTimeout = y.TCP.DetectTimeout
		tcpIdleTimeout = y.TCP.IdleTimeout
		headerVersion = y.TCP.HeaderVersion
		familyMismatch = y.TCP.FamilyMismatch
		if y.TCP.MaxConnections != nil {
			maxConnections = *y.TCP.MaxConnections
		}
	}
	if detectTimeout == nil {
		if v, ok := raw["detect-timeout"].(string); ok {
			detectTimeout = &v
		}
	}
	// family-mismatch has no pre-v3 equivalent: configs written before this
	// field existed were served by the historical auto-conversion behavior
	// (mixed address-family headers re-encoded as-is, including ::ffff:
	// mapped IPv4 under AF_INET6). Inject "legacy" explicitly — rather than
	// letting the fresh-config default "reject" apply — so a version upgrade
	// never changes downstream wire behavior silently; operators opt into the
	// stricter reject/unknown deliberately after reading the startup warning.
	// idle-timeout below gets back-filled with its default for a similar
	// reason: v0.3.2 introduced it without a version bump, so older
	// version-2 files never gained the line and were never migrated.
	if familyMismatch == nil {
		legacy := "legacy"
		familyMismatch = &legacy
	}
	b.WriteString("\n# ── TCP (protocol=tcp) ───────────────────────────────────────────────\n")
	b.WriteString("tcp:\n")
	writeStrFieldIndent(&b, "detect-timeout", detectTimeout, "1s", "PROXY header detection timeout (0=block indefinitely)", "  ")
	writeStrFieldIndent(&b, "idle-timeout", tcpIdleTimeout, "5m", "pipe idle timeout, 0=disabled", "  ")
	writeStrFieldIndent(&b, "header-version", headerVersion, "v2", "downstream PROXY header version: v1|v2", "  ")
	writeStrFieldIndent(&b, "family-mismatch", familyMismatch, "reject", "mixed address-family action: reject|unknown|legacy", "  ")
	fmt.Fprintf(&b, "  max-connections: %d  # max concurrent connections, 0=unlimited\n", maxConnections)

	// ── UDP
	var idleTimeout *string
	var maxSessions *int
	var maxDatagramSize *int
	var headerMode *string
	if y.UDP != nil {
		idleTimeout = y.UDP.IdleTimeout
		maxSessions = y.UDP.MaxSessions
		maxDatagramSize = y.UDP.MaxDatagramSize
		headerMode = y.UDP.HeaderMode
	}
	// v1 flat field fallbacks: if the nested v2 value is absent, check
	// whether the old config had the value as a flat top-level key.
	if idleTimeout == nil {
		if v, ok := raw["idle-timeout"].(string); ok {
			idleTimeout = &v
		}
	}
	if maxSessions == nil {
		if n, ok := rawInt(raw, "max-sessions"); ok {
			maxSessions = &n
		}
	}
	if maxDatagramSize == nil {
		if n, ok := rawInt(raw, "max-datagram-size"); ok {
			maxDatagramSize = &n
		}
	}
	if headerMode == nil {
		if v, ok := raw["udp-output"].(string); ok { // v1 name
			headerMode = &v
		} else if v, ok := raw["header-mode"].(string); ok { // flat v2 name
			headerMode = &v
		}
	}
	b.WriteString("\n# ── UDP (protocol=udp) ───────────────────────────────────────────────\n")
	b.WriteString("# The following fields are only used when protocol=udp.\n")
	b.WriteString("udp:\n")
	writeStrFieldIndent(&b, "idle-timeout", idleTimeout, "30s", "UDP session idle timeout", "  ")
	ms := 1024
	if maxSessions != nil {
		ms = *maxSessions
	}
	fmt.Fprintf(&b, "  max-sessions: %d  # max concurrent UDP sessions, 0=unlimited\n", ms)
	mds := 65535
	if maxDatagramSize != nil {
		mds = *maxDatagramSize
	}
	fmt.Fprintf(&b, "  max-datagram-size: %d  # max datagram size, 0=unlimited, oversized=drop\n", mds)
	writeStrFieldIndent(&b, "header-mode", headerMode, "every_datagram", "every_datagram (default) | first_datagram", "  ")

	// ── Logging
	var cl, cf, fl, ff, fp *string
	if y.Log != nil && y.Log.Console != nil {
		cl, cf = y.Log.Console.Level, y.Log.Console.Format
	}
	if y.Log != nil && y.Log.File != nil {
		fp, fl, ff = y.Log.File.Path, y.Log.File.Level, y.Log.File.Format
	}
	b.WriteString("\n# ── Logging ──────────────────────────────────────────────────────────\n")
	b.WriteString("log:\n")
	b.WriteString("  console:  # logs to stderr\n")
	writeStrFieldIndent(&b, "level", cl, "info", "debug | info | warn | error", "    ")
	writeStrFieldIndent(&b, "format", cf, "text", "text | json", "    ")
	b.WriteString("  file:  # logs to a file (path empty => disabled)\n")
	writeStrFieldIndent(&b, "path", fp, "", "e.g. /var/log/proxydge.log", "    ")
	flVal := "info"
	if fl != nil {
		flVal = *fl
	}
	ffVal := "text"
	if ff != nil {
		ffVal = *ff
	}
	fmt.Fprintf(&b, "    level: %q\n", flVal)
	fmt.Fprintf(&b, "    format: %q\n", ffVal)

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

// rawInt reads an integer from the raw YAML map, handling int and float64
// types that different YAML parsers may produce.
func rawInt(raw map[string]any, key string) (int, bool) {
	v, ok := raw[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
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
	if v, ok := os.LookupEnv(envPrefix + "TCP_DETECT_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%sTCP_DETECT_TIMEOUT=%q: %w", envPrefix, v, err)
		}
		c.DetectTimeout = d
		c.mark(fDetectTimeout, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "TCP_IDLE_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%sTCP_IDLE_TIMEOUT=%q: %w", envPrefix, v, err)
		}
		c.TCPIdleTimeout = d
		c.mark(fTCPIdleTimeout, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "TCP_HEADER_VERSION"); ok && v != "" {
		c.TCPHeaderVersion = v
		c.mark(fTCPHeaderVersion, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "TCP_FAMILY_MISMATCH"); ok && v != "" {
		c.TCPFamilyMismatch = v
		c.mark(fTCPFamilyMismatch, "env")
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
	if v, ok := os.LookupEnv(envPrefix + "UDP_IDLE_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%sUDP_IDLE_TIMEOUT=%q: %w", envPrefix, v, err)
		}
		c.IdleTimeout = d
		c.mark(fIdleTimeout, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UDP_MAX_SESSIONS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%sUDP_MAX_SESSIONS=%q: %w", envPrefix, v, err)
		}
		c.MaxSessions = n
		c.mark(fMaxSessions, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UDP_MAX_DATAGRAM_SIZE"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%sUDP_MAX_DATAGRAM_SIZE=%q: %w", envPrefix, v, err)
		}
		c.MaxDatagramSize = n
		c.mark(fMaxDatagramSize, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UDP_HEADER_MODE"); ok && v != "" {
		c.UDPHeaderMode = v
		c.mark(fUDPHeaderMode, "env")
	}
	return nil
}

// --- flag source ---

// flagValues are the parsed flag pointers (zero defaults: the flag source only
// applies flags that were explicitly set).
type flagValues struct {
	listen, upstream, policy, config         *string
	detectTimeout                            *time.Duration
	tcpIdleTimeout                           *time.Duration
	tcpHeaderVersion, tcpFamilyMismatch      *string
	tcpMaxConnections                        *int
	lang                                     *string
	logConsoleLevel, logConsoleFormat        *string
	logFilePath, logFileLevel, logFileFormat *string
	trustedNetworks                          *string
	untrustedProxyAction                     *string
	protocol                                 *string
	idleTimeout                              *time.Duration
	maxSessions                              *int
	maxDatagramSize                          *int
	udpHeaderMode                            *string
}

func parseFlags(args []string) (*flagValues, map[string]bool, error) {
	fv := &flagValues{}
	fs := flag.NewFlagSet("proxydge start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fv.listen = fs.String("listen", "", "listen address (host:port)")
	fv.upstream = fs.String("upstream", "", "downstream target host:port (default 127.0.0.1:9001)")
	fv.policy = fs.String("policy", "", "upstream header policy: use|require|reject")
	fv.config = fs.String("config", "", "config file path (overrides exe-dir config.yaml)")
	fv.detectTimeout = fs.Duration("tcp-detect-timeout", 0, "PROXY header detection timeout (0=block indefinitely)")
	fv.tcpIdleTimeout = fs.Duration("tcp-idle-timeout", 0, "pipe idle timeout (0=disabled, default 5m)")
	fv.tcpHeaderVersion = fs.String("tcp-header-version", "", "downstream PROXY header version: v1|v2 (default v2)")
	fv.tcpFamilyMismatch = fs.String("tcp-family-mismatch", "", "mixed address-family action: reject|unknown|legacy (default reject)")
	fv.tcpMaxConnections = fs.Int("tcp-max-connections", 0, "max concurrent connections (default 4096)")
	fv.lang = fs.String("lang", "", "display language: en|zh-CN|zh-TW (default auto)")
	fv.logConsoleLevel = fs.String("log-console-level", "", "console log level: debug|info|warn|error")
	fv.logConsoleFormat = fs.String("log-console-format", "", "console log format: text|json")
	fv.logFilePath = fs.String("log-file", "", "file log path (empty=disabled)")
	fv.logFileLevel = fs.String("log-file-level", "", "file log level: debug|info|warn|error")
	fv.logFileFormat = fs.String("log-file-format", "", "file log format: text|json")
	fv.trustedNetworks = fs.String("trusted-networks", "", "trusted networks (comma-separated CIDRs, empty=all)")
	fv.untrustedProxyAction = fs.String("untrusted-proxy-action", "", "action for untrusted sources with PROXY header: reject|strip")
	fv.protocol = fs.String("protocol", "", "transport protocol: tcp|udp")
	fv.idleTimeout = fs.Duration("udp-idle-timeout", 0, "UDP session idle timeout")
	fv.maxSessions = fs.Int("udp-max-sessions", 0, "max concurrent UDP sessions, 0=unlimited (default 1024)")
	fv.maxDatagramSize = fs.Int("udp-max-datagram-size", 0, "max datagram size (0=unlimited)")
	fv.udpHeaderMode = fs.String("udp-header-mode", "", "UDP header mode: every_datagram|first_datagram")
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
	if s.set["tcp-detect-timeout"] {
		c.DetectTimeout = *s.fv.detectTimeout
		c.mark(fDetectTimeout, "flag")
	}
	if s.set["tcp-idle-timeout"] {
		c.TCPIdleTimeout = *s.fv.tcpIdleTimeout
		c.mark(fTCPIdleTimeout, "flag")
	}
	if s.set["tcp-header-version"] {
		c.TCPHeaderVersion = *s.fv.tcpHeaderVersion
		c.mark(fTCPHeaderVersion, "flag")
	}
	if s.set["tcp-family-mismatch"] {
		c.TCPFamilyMismatch = *s.fv.tcpFamilyMismatch
		c.mark(fTCPFamilyMismatch, "flag")
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
	if s.set["udp-idle-timeout"] {
		c.IdleTimeout = *s.fv.idleTimeout
		c.mark(fIdleTimeout, "flag")
	}
	if s.set["udp-max-sessions"] {
		c.MaxSessions = *s.fv.maxSessions
		c.mark(fMaxSessions, "flag")
	}
	if s.set["udp-max-datagram-size"] {
		c.MaxDatagramSize = *s.fv.maxDatagramSize
		c.mark(fMaxDatagramSize, "flag")
	}
	if s.set["udp-header-mode"] {
		c.UDPHeaderMode = *s.fv.udpHeaderMode
		c.mark(fUDPHeaderMode, "flag")
	}
	return nil
}
