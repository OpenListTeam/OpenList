package mcp

import (
	"encoding/json"
	"testing"
)

func TestParseFSGetArgsRequiresPath(t *testing.T) {
	_, err := parseFSGetArgs(json.RawMessage(`{"password":"secret"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Code != -32602 {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func TestParseFSGetArgs(t *testing.T) {
	args, err := parseFSGetArgs(json.RawMessage(`{"path":"/movie.mkv","password":"secret"}`))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if args.Path != "/movie.mkv" {
		t.Fatalf("unexpected path: %q", args.Path)
	}
	if args.Password != "secret" {
		t.Fatalf("unexpected password: %q", args.Password)
	}
}

func TestParseFSGetArgsRejectsInvalidJSON(t *testing.T) {
	_, err := parseFSGetArgs(json.RawMessage(`"bad"`))
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Code != -32602 {
		t.Fatalf("unexpected error: %+v", err)
	}
}
