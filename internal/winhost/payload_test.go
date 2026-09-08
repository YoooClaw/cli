package winhost

import (
	"bytes"
	"debug/pe"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedHostUsesGUISubsystem(t *testing.T) {
	if len(payload) == 0 {
		t.Skip("release/nativehost build only")
	}
	file, err := pe.NewFile(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	header, ok := file.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || header.Subsystem != 2 {
		t.Fatal("embedded host must be a 64-bit Windows GUI executable, not a console host")
	}
}

func TestContentAddressedHostIsReusedAndVersionsDoNotOverwrite(t *testing.T) {
	original := payload
	defer func() { payload = original }()
	payload = []byte("test native host v1")
	root := t.TempDir()
	first, err := Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Ensure(root)
	if err != nil || again != first {
		t.Fatalf("reuse=%s err=%v", again, err)
	}
	statAgain, _ := os.Stat(first)
	if !stat.ModTime().Equal(statAgain.ModTime()) {
		t.Fatal("unchanged host was rewritten")
	}
	payload = []byte("test native host v2")
	second, err := Ensure(root)
	if err != nil || second == first {
		t.Fatalf("version path=%s err=%v", second, err)
	}
	if filepath.Dir(first) != filepath.Join(root, "hosts") {
		t.Fatal("host escaped scoped directory")
	}
	if b, _ := os.ReadFile(first); string(b) != "test native host v1" {
		t.Fatal("old running host overwritten")
	}
}
