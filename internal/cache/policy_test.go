package cache

import (
	"encoding/json"
	"testing"

	"github.com/caarlos0/env/v9"
)

func TestParsePolicy(t *testing.T) {
	tests := []struct {
		input string
		want  Policy
	}{
		{"", PolicyAuto},
		{"auto", PolicyAuto},
		{" MEMORY ", PolicyMemory},
		{"Disk", PolicyDisk},
	}
	for _, tt := range tests {
		got, err := ParsePolicy(tt.input)
		if err != nil {
			t.Fatalf("ParsePolicy(%q) error = %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("ParsePolicy(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
	if _, err := ParsePolicy("hybrid"); err == nil {
		t.Fatal("ParsePolicy() expected an error for an invalid policy")
	}
}

func TestPolicyEnvironment(t *testing.T) {
	t.Setenv("OPENLIST_TEST_CACHE_POLICY", " Disk ")
	var cfg struct {
		Policy Policy `env:"CACHE_POLICY"`
	}
	if err := env.ParseWithOptions(&cfg, env.Options{Prefix: "OPENLIST_TEST_"}); err != nil {
		t.Fatalf("env.ParseWithOptions() error = %v", err)
	}
	if cfg.Policy != PolicyDisk {
		t.Fatalf("environment policy = %q, want disk", cfg.Policy)
	}
}

func TestResolvePolicy(t *testing.T) {
	got, err := ResolvePolicy(PolicyInherit, PolicyDisk)
	if err != nil || got != PolicyDisk {
		t.Fatalf("ResolvePolicy(inherit, disk) = %q, %v", got, err)
	}
	got, err = ResolvePolicy(PolicyMemory, PolicyDisk)
	if err != nil || got != PolicyMemory {
		t.Fatalf("ResolvePolicy(memory, disk) = %q, %v", got, err)
	}
	if _, err := ResolvePolicy(PolicyInherit, Policy("invalid")); err == nil {
		t.Fatal("ResolvePolicy() expected an error for an invalid fallback")
	}
}

func TestPolicyJSON(t *testing.T) {
	type config struct {
		Policy Policy `json:"cache_policy"`
	}
	var cfg config
	if err := json.Unmarshal([]byte(`{"cache_policy":" MEMORY "}`), &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if cfg.Policy != PolicyMemory {
		t.Fatalf("json.Unmarshal() policy = %q, want memory", cfg.Policy)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(b) != `{"cache_policy":"memory"}` {
		t.Fatalf("json.Marshal() = %s", b)
	}
	if err := json.Unmarshal([]byte(`{"cache_policy":"invalid"}`), &cfg); err == nil {
		t.Fatal("json.Unmarshal() expected an error for an invalid policy")
	}
}
