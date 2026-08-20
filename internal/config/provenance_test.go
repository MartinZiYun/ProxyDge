package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProvenanceDefaultsAndFlag(t *testing.T) {
	c, err := Load([]string{"-upstream", "1.2.3.4:80"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.sourceOf(fListen) != "default" {
		t.Fatalf("listen source: want default, got %q", c.sourceOf(fListen))
	}
	if c.sourceOf(fUpstream) != "flag" {
		t.Fatalf("upstream source: want flag, got %q", c.sourceOf(fUpstream))
	}
	if c.sourceOf(fPolicy) != "default" {
		t.Fatalf("policy source: want default, got %q", c.sourceOf(fPolicy))
	}
	if c.loadedFile != "" {
		t.Fatalf("loadedFile: want empty, got %q", c.loadedFile)
	}
}

func TestProvenanceEnvBeatsFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "upstream: 1.1.1.1:1\nlog:\n  console:\n    level: debug\n")
	t.Setenv("PROXYDGE_UPSTREAM", "2.2.2.2:2") // env beats file
	c, err := Load([]string{"-config", p})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.sourceOf(fUpstream) != "env" {
		t.Fatalf("upstream source: want env, got %q", c.sourceOf(fUpstream))
	}
	if c.sourceOf(fLogConsoleLevel) != "file "+p {
		t.Fatalf("console level source: want file, got %q", c.sourceOf(fLogConsoleLevel))
	}
	if c.loadedFile != p {
		t.Fatalf("loadedFile: want %q, got %q", p, c.loadedFile)
	}
}

func TestProvenanceFlagBeatsEnv(t *testing.T) {
	t.Setenv("PROXYDGE_UPSTREAM", "2.2.2.2:2")
	c, err := Load([]string{"-upstream", "3.3.3.3:3"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.sourceOf(fUpstream) != "flag" {
		t.Fatalf("upstream source: want flag, got %q", c.sourceOf(fUpstream))
	}
}

func TestProvenanceFileFieldsAll(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "listen: 1.1.1.1:1\nupstream: 2.2.2.2:2\npolicy: require\ndetect-timeout: 250ms\nlog:\n  console:\n    level: debug\n    format: json\n  file:\n    path: /tmp/x.log\n    level: warn\n    format: json\n")
	c, err := Load([]string{"-config", p})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := "file " + p
	for _, f := range []string{fListen, fUpstream, fPolicy, fDetectTimeout, fLogConsoleLevel, fLogConsoleFormat, fLogFilePath, fLogFileLevel, fLogFileFormat} {
		if got := c.sourceOf(f); got != want {
			t.Errorf("%s source: want %q, got %q", f, want, got)
		}
	}
}

func TestDescribeContainsSources(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	writeFile(t, dir, "c.yaml", "upstream: 1.2.3.4:80\n")
	t.Setenv("PROXYDGE_POLICY", "reject")
	c, err := Load([]string{"-config", p, "-log-file", "/tmp/x.log"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	desc := c.Describe()
	if !strings.Contains(desc, "config file: "+p) {
		t.Fatalf("describe missing config file line:\n%s", desc)
	}
	if !strings.Contains(desc, "upstream = 1.2.3.4:80 (file "+p+")") {
		t.Fatalf("describe missing upstream provenance:\n%s", desc)
	}
	if !strings.Contains(desc, "policy = reject (env)") {
		t.Fatalf("describe missing policy env provenance:\n%s", desc)
	}
	if !strings.Contains(desc, "log.file.path = /tmp/x.log (flag)") {
		t.Fatalf("describe missing log.file.path flag provenance:\n%s", desc)
	}
	if !strings.Contains(desc, "listen = :9000 (default)") {
		t.Fatalf("describe missing listen default provenance:\n%s", desc)
	}
}

func TestDescribeNoFile(t *testing.T) {
	c, err := Load([]string{"-upstream", "1.2.3.4:80"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	desc := c.Describe()
	if !strings.Contains(desc, "config file: (none)") {
		t.Fatalf("describe should show (none) when no file loaded:\n%s", desc)
	}
}
