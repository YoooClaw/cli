package recording

import "testing"

func TestRenameUpdatesDisplayTitle(t *testing.T) {
	for _, capture := range []bool{false, true} {
		storage := NewStorage(t.TempDir(), testLogger{t})
		if capture {
			storage = NewCaptureStorage(t.TempDir(), testLogger{t})
		}
		if err := storage.Init(); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.Ingest("rec", Metadata{Name: "原名"}, "phone"); err != nil {
			t.Fatal(err)
		}
		if err := storage.SetTitle("rec", "转写生成的旧标题"); err != nil {
			t.Fatal(err)
		}
		entry, found, err := storage.Rename("rec", "用户新名称")
		if err != nil || !found || entry.Title != "用户新名称" || entry.Metadata.Name != "用户新名称" {
			t.Fatalf("rename: %+v %v", entry, err)
		}
		entry, found, err = storage.LookupByID("rec")
		if err != nil || !found || entry.Title != "用户新名称" {
			t.Fatalf("persisted: %+v %v", entry, err)
		}
	}
}
