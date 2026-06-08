package creds

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/config"
)

func TestResolveRefEnv(t *testing.T) {
	t.Setenv("MY_TOKEN", "  secret-val  ")
	got, err := ResolveRef("env:MY_TOKEN")
	if err != nil || got.Source != "env" || got.Value != "secret-val" {
		t.Errorf("env ref: %+v err=%v", got, err)
	}
}

func TestResolveRefInline(t *testing.T) {
	t.Parallel()
	enc := base64.StdEncoding.EncodeToString([]byte("hello"))
	got, err := ResolveRef("inline:" + enc)
	if err != nil || got.Value != "hello" || got.Source != "inline" {
		t.Errorf("inline ref: %+v err=%v", got, err)
	}
}

func TestResolveRefFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(path, []byte(`{"gatewayToken":"tok-123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRef("file:" + path + "#gatewayToken")
	if err != nil || got.Value != "tok-123" || got.Source != "file" {
		t.Errorf("file ref: %+v err=%v", got, err)
	}
	// 缺字段 -> 空值，不报错
	empty, err := ResolveRef("file:" + path + "#missing")
	if err != nil || empty.Value != "" {
		t.Errorf("missing field should be empty: %+v err=%v", empty, err)
	}
}

func TestResolveRefErrors(t *testing.T) {
	t.Parallel()
	bad := []string{
		"no-colon",
		"file:/path/without/hash",
		"keychain:no-slash",
		"weird:rest",
	}
	for _, ref := range bad {
		if _, err := ResolveRef(ref); err == nil {
			t.Errorf("ResolveRef(%q) should error", ref)
		}
	}
}

func TestResolveRefKeychain(t *testing.T) {
	fk := setup(t)
	fk.store[key("svc", "acct")] = "kc-secret"
	got, err := ResolveRef("keychain:svc/acct")
	if err != nil || got.Value != "kc-secret" || got.Source != "keychain" {
		t.Errorf("keychain ref: %+v err=%v", got, err)
	}
}

func TestWriteRefFileRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ref := "file:" + filepath.Join(dir, "c.json") + "#token"
	if _, err := WriteRef(ref, "written-value"); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRef(ref)
	if err != nil || got.Value != "written-value" {
		t.Errorf("write/resolve round trip: %+v err=%v", got, err)
	}
}

func TestWriteRefKeychain(t *testing.T) {
	fk := setup(t)
	if _, err := WriteRef("keychain:svc/acct", "v1"); err != nil {
		t.Fatal(err)
	}
	if fk.store[key("svc", "acct")] != "v1" {
		t.Error("keychain write not stored")
	}
	// 不可用时报错
	fk.available = false
	if _, err := WriteRef("keychain:svc/acct", "v2"); err == nil {
		t.Error("unavailable keychain write should error")
	}
}

func TestWriteRefRejectsEnvInline(t *testing.T) {
	t.Parallel()
	if _, err := WriteRef("env:X", "v"); err == nil {
		t.Error("env writes should be rejected")
	}
	if _, err := WriteRef("inline:abc", "v"); err == nil {
		t.Error("inline writes should be rejected")
	}
	if _, err := WriteRef("no-colon", "v"); err == nil {
		t.Error("malformed ref should error")
	}
	if _, err := WriteRef("bogus:rest", "v"); err == nil {
		t.Error("unknown scheme should error")
	}
}

func TestExpandHome(t *testing.T) {
	t.Parallel()
	home, _ := os.UserHomeDir()
	if got := expandHome("~"); got != home {
		t.Errorf("~ = %q, want %q", got, home)
	}
	if got := expandHome("~/sub"); got != filepath.Join(home, "sub") {
		t.Errorf("~/sub = %q", got)
	}
	if got := expandHome("/abs"); got != "/abs" {
		t.Errorf("abs path should be unchanged: %q", got)
	}
}

func TestGatewayTokenHelpers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Config{Auth: config.AuthSection{TokenRef: "file:" + filepath.Join(dir, "c.json") + "#gatewayToken"}}
	if _, err := WriteGatewayToken(cfg, "gw-tok"); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveGatewayToken(cfg)
	if err != nil || got.Value != "gw-tok" {
		t.Errorf("gateway token round trip: %+v err=%v", got, err)
	}
}
