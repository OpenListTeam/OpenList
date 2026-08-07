package mcp

import (
	"encoding/json"
	"testing"
)

func TestParseFSListArgsRequiresPath(t *testing.T) {
	assertFSListPathRequired(t, json.RawMessage(`{"refresh":true}`))
}

func TestParseFSListArgsRejectsEmptyArguments(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		assertFSListInvalidArguments(t, raw)
	}
}

func TestParseFSListArgsRejectsInvalidJSON(t *testing.T) {
	assertFSListInvalidArguments(t, json.RawMessage(`"bad"`))
}

func TestParseFSListArgs(t *testing.T) {
	args, err := parseFSListArgs(json.RawMessage(`{"path":"/movies","password":"secret","refresh":true,"page":2,"per_page":10}`))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if args.Path != "/movies" || args.Password != "secret" || !args.Refresh || args.Page != 2 || args.PerPage != 10 {
		t.Fatalf("unexpected args: %+v", args)
	}
}

func assertFSListPathRequired(t *testing.T, raw json.RawMessage) {
	t.Helper()

	_, err := parseFSListArgs(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Code != -32602 || err.Message != "path is required" {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func assertFSListInvalidArguments(t *testing.T, raw json.RawMessage) {
	t.Helper()

	_, err := parseFSListArgs(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Code != -32602 || err.Message != "invalid openlist.fs.list arguments" {
		t.Fatalf("unexpected error: %+v", err)
	}
}
