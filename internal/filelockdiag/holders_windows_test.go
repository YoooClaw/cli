//go:build windows

package filelockdiag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupFindsCurrentProcessHoldingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, []byte(`{"recordings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Go 在 Windows 上的普通只读打开不包含 FILE_SHARE_DELETE，正好模拟
	// 会阻止 daemon 原子替换 index.json 的读取者。
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	holders, supported, err := Lookup(path)
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Fatal("Windows Lookup reported unsupported")
	}
	wantPID := uint32(os.Getpid())
	for _, holder := range holders {
		if holder.PID == wantPID {
			return
		}
	}
	t.Fatalf("current process %d not found in holders: %+v", wantPID, holders)
}
