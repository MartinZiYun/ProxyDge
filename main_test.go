package main

import "testing"

func TestRunRequiresUpstream(t *testing.T) {
	if code := run([]string{"-listen", "127.0.0.1:0"}); code != 2 {
		t.Fatalf("missing -upstream: want exit 2, got %d", code)
	}
}

func TestRunInvalidPolicy(t *testing.T) {
	if code := run([]string{"-upstream", "127.0.0.1:1", "-policy", "bogus"}); code != 2 {
		t.Fatalf("invalid -policy: want exit 2, got %d", code)
	}
}

func TestRunValidPolicyAccepted(t *testing.T) {
	// Valid flags must reach listen/serve; use a bad listen to force a
	// runtime exit (1) and confirm the flag parsing succeeded (not 2).
	if code := run([]string{"-upstream", "127.0.0.1:1", "-policy", "use", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("bad listen: want exit 1 (runtime), got %d", code)
	}
}
