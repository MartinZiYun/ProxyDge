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
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime configuration. It is the single object every Source
// overlays onto; it is also what the rest of the program consumes. main maps
// its fields into gateway.New — the gateway never imports this package.
type Config struct {
	Listen        string
	Upstream      string
	Policy        string
	DetectTimeout time.Duration
	ConfigPath    string // resolved config file path (meta; not validated)

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

// configField pairs a field's display name with a value accessor, for Describe.
type configField struct {
	name  string
	value func(*Config) any
}

// configFields is the single source of truth for field names + display order.
// Sources mark fields using these exact names; Describe prints them in order.
var configFields = []configField{
	{"listen", func(c *Config) any { return c.Listen }},
	{"upstream", func(c *Config) any { return c.Upstream }},
	{"policy", func(c *Config) any { return c.Policy }},
	{"detect-timeout", func(c *Config) any { return c.DetectTimeout }},
	{"log.console.level", func(c *Config) any { return c.LogConsoleLevel }},
	{"log.console.format", func(c *Config) any { return c.LogConsoleFormat }},
	{"log.file.path", func(c *Config) any { return c.LogFilePath }},
	{"log.file.level", func(c *Config) any { return c.LogFileLevel }},
	{"log.file.format", func(c *Config) any { return c.LogFileFormat }},
}

// fieldName constants keep the Sources' mark() calls aligned with configFields.
const (
	fListen            = "listen"
	fUpstream          = "upstream"
	fPolicy            = "policy"
	fDetectTimeout     = "detect-timeout"
	fLogConsoleLevel   = "log.console.level"
	fLogConsoleFormat  = "log.console.format"
	fLogFilePath       = "log.file.path"
	fLogFileLevel      = "log.file.level"
	fLogFileFormat     = "log.file.format"
)

// Describe returns a human-readable dump of every config field with its value
// and the source that provided it, plus the config file that was loaded (if
// any). Intended for the startup banner.
func (c *Config) Describe() string {
	var b strings.Builder
	b.WriteString("config file: ")
	if c.loadedFile != "" {
		b.WriteString(c.loadedFile)
	} else {
		b.WriteString("(none)")
	}
	b.WriteByte('\n')
	for _, f := range configFields {
		fmt.Fprintf(&b, "%s = %v (%s)\n", f.name, f.value(c), c.sourceOf(f.name))
	}
	return b.String()
}

// sourceOf returns the source label for a field (defaults to "default").
func (c *Config) sourceOf(field string) string {
	if s, ok := c.prov[field]; ok {
		return s
	}
	return "default"
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

listen: ":9000"          # listen address (host:port)
upstream: ""             # REQUIRED: downstream target host:port, e.g. 127.0.0.1:9001
policy: "use"            # use | require | reject
detect-timeout: "1s"     # PROXY header detection timeout

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
	// LogFilePath defaults to "" (file sink off).
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
	Listen        *string `yaml:"listen"`
	Upstream      *string `yaml:"upstream"`
	Policy        *string `yaml:"policy"`
	DetectTimeout *string `yaml:"detect-timeout"`
	Log           *yamlLog `yaml:"log"`
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
	// A file was actually loaded — record it for the startup banner.
	c.loadedFile = s.path
	src := "file " + s.path
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
	return nil
}

// --- flag source ---

// flagValues are the parsed flag pointers (zero defaults: the flag source only
// applies flags that were explicitly set).
type flagValues struct {
	listen, upstream, policy, config        *string
	detectTimeout                            *time.Duration
	logConsoleLevel, logConsoleFormat       *string
	logFilePath, logFileLevel, logFileFormat *string
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
	fv.logConsoleLevel = fs.String("log-console-level", "", "console log level: debug|info|warn|error")
	fv.logConsoleFormat = fs.String("log-console-format", "", "console log format: text|json")
	fv.logFilePath = fs.String("log-file", "", "file log path (empty=disabled)")
	fv.logFileLevel = fs.String("log-file-level", "", "file log level: debug|info|warn|error")
	fv.logFileFormat = fs.String("log-file-format", "", "file log format: text|json")
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
	return nil
}
