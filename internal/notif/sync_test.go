package notif

import (
	"testing"
)

func makeItems(n int) []StoredNotification {
	items := make([]StoredNotification, n)
	for i := range items {
		items[i] = StoredNotification{AppName: "x", Title: "t", Content: "c", Timestamp: "2026-06-07T10:00:00Z"}
	}
	return items
}

func TestScanSyncEmpty(t *testing.T) {
	t.Parallel()
	res := ScanSync(t.TempDir())
	if res["totalPending"].(int) != 0 {
		t.Errorf("empty scan should have 0 pending: %+v", res)
	}
}

func TestScanSyncPending(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(5))
	res := ScanSync(dir)
	if res["totalPending"].(int) != 5 {
		t.Errorf("expected 5 pending: %+v", res)
	}
	pending := res["pending"].([]SyncPending)
	if len(pending) != 1 || pending[0].StartIndex != 0 || pending[0].Count != 5 {
		t.Errorf("pending detail wrong: %+v", pending)
	}
}

func TestFetchSyncMissingDate(t *testing.T) {
	t.Parallel()
	if _, err := FetchSync(t.TempDir(), "2026-06-07", ""); err == nil {
		t.Error("missing date should error")
	}
}

func TestFetchSync(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(3))
	res, err := FetchSync(dir, "2026-06-07", "")
	if err != nil {
		t.Fatal(err)
	}
	if res["startIndex"].(int) != 0 || res["endIndex"].(int) != 2 || res["returned"].(int) != 3 {
		t.Errorf("fetch result wrong: %+v", res)
	}
	if res["hasMore"].(bool) {
		t.Error("should not have more")
	}
}

func TestFetchSyncMaxEndIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(10))
	res, err := FetchSync(dir, "2026-06-07", "2")
	if err != nil {
		t.Fatal(err)
	}
	if res["returned"].(int) != 3 || res["maxEndIndex"].(int) != 2 {
		t.Errorf("max-end-index not applied: %+v", res)
	}
}

func TestFetchSyncInvalidMaxEndIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(3))
	if _, err := FetchSync(dir, "2026-06-07", "-1"); err == nil {
		t.Error("negative max-end-index should error")
	}
}

func TestCommitSyncLegacyBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(3))
	res, err := CommitSync(dir, "2026-06-07", "")
	if err != nil {
		t.Fatal(err)
	}
	if res["committedIndex"].(int) != 2 || res["commitMode"] != "legacy-batch-limit" || res["hasMore"].(bool) {
		t.Errorf("legacy commit wrong: %+v", res)
	}
	// checkpoint 持久化后再 scan 应无 pending
	if ScanSync(dir)["totalPending"].(int) != 0 {
		t.Error("after full commit no pending expected")
	}
}

func TestCommitSyncExactEndIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(5))
	res, err := CommitSync(dir, "2026-06-07", "1")
	if err != nil {
		t.Fatal(err)
	}
	if res["committedIndex"].(int) != 1 || res["commitMode"] != "exact-end-index" || !res["hasMore"].(bool) {
		t.Errorf("exact commit wrong: %+v", res)
	}
}

func TestCommitSyncErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(3))
	// 超范围
	if _, err := CommitSync(dir, "2026-06-07", "99"); err == nil {
		t.Error("out-of-range end-index should error")
	}
	// 缺数据
	if _, err := CommitSync(dir, "2026-01-01", "0"); err == nil {
		t.Error("missing date should error")
	}
	// 非法
	if _, err := CommitSync(dir, "2026-06-07", "abc"); err == nil {
		t.Error("non-numeric end-index should error")
	}
}

func TestCommitSyncNoBackward(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-07", makeItems(5))
	if _, err := CommitSync(dir, "2026-06-07", "3"); err != nil {
		t.Fatal(err)
	}
	// 回退到更小的 index 应被拒绝
	if _, err := CommitSync(dir, "2026-06-07", "1"); err == nil {
		t.Error("backward commit should error")
	}
}

func TestSafeSlice(t *testing.T) {
	t.Parallel()
	items := makeItems(5)
	if got := safeSlice(items, -1, 100); len(got) != 5 {
		t.Errorf("out of range slice should clamp: %d", len(got))
	}
	if got := safeSlice(items, 3, 1); len(got) != 0 {
		t.Errorf("reversed slice should be empty: %d", len(got))
	}
	if got := safeSlice(items, 10, 12); len(got) != 0 {
		t.Errorf("start beyond len should be empty: %d", len(got))
	}
}
