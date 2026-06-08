package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update 控制 golden 文件刷新：go test ./... -update 时重写期望值。
var update = flag.Bool("update", false, "刷新 golden 测试的期望文件（testdata/*.golden）")

// Golden 比对 got 与 testdata/<name>.golden 的内容。
// 带 -update 时把 got 写回 golden 文件并通过；否则不一致即 t.Errorf。
//
//	body := light.BuildLightEffectApnsBody(...)
//	testutil.Golden(t, "preset_breathing", []byte(body))
func Golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("testutil.Golden: mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("testutil.Golden: 写入 golden 失败: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testutil.Golden: 读取 %s 失败（首次运行请加 -update）: %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("golden 不一致 %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
