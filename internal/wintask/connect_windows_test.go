//go:build windows

package wintask

import (
	"github.com/go-ole/go-ole"
	"os"
	"testing"
	"unsafe"
)

func TestNativeConnectLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit tool")
	}
	if unsafe.Offsetof(taskServiceVtbl{}.Connect) != 10*8 {
		t.Fatal("ITaskService Connect slot changed")
	}
	if unsafe.Sizeof(ole.VARIANT{}) != 24 {
		t.Fatal("unexpected Windows 64-bit VARIANT size")
	}
}

// Opt-in, strictly read-only Windows smoke test. No registration or UAC.
func TestReadOnlyTaskScheduler(t *testing.T) {
	if os.Getenv("YOOOCLAW_REPAIR_COM_TEST") != "1" {
		t.Skip("requires Windows Task Scheduler; opt in explicitly")
	}
	s, err := Open(t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.XML(); err != nil {
		t.Fatal(err)
	}
}
