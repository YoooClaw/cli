package data

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommitHookDistinguishesBlobReuseAndIdempotentRetry(t *testing.T) {
	s := uploadStore(t)
	var events []Object
	s.OnCommit = func(o Object) {
		// Callback runs after index commit and outside the store lock.
		got, e := s.Show(o.ID, o.ClientLabel)
		if e != nil || got.ID != o.ID {
			t.Error(got, e)
		}
		p, e := s.Path(o)
		if e != nil {
			t.Error(e)
		}
		if _, e = os.ReadFile(p); e != nil {
			t.Error(e)
		}
		events = append(events, o)
	}
	raw := []byte("same bytes")
	a, e := s.Put(bytes.NewReader(raw), Metadata{IdempotencyKey: "once"}, "a")
	if e != nil {
		t.Fatal(e)
	}
	retry, e := s.Put(bytes.NewReader(raw), Metadata{IdempotencyKey: "once"}, "a")
	if e != nil || retry.Data.ID != a.Data.ID {
		t.Fatal(retry, e)
	}
	b, e := s.Put(bytes.NewReader(raw), Metadata{}, "a")
	if e != nil || !b.Deduped || b.Data.ID == a.Data.ID {
		t.Fatal(b, e)
	}
	if len(events) != 2 {
		t.Fatal(events)
	}
	u := NewUploads(s)
	st, e := u.Begin(uploadRequest(raw, "upload"), "a", "identity")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = u.Commit(st.UploadID, "a", "identity"); e == nil {
		t.Fatal("incomplete upload committed")
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 0, raw); e != nil {
		t.Fatal(e)
	}
	if len(events) != 2 {
		t.Fatal("chunk emitted")
	}
	for i := 0; i < 2; i++ {
		if _, e = u.Commit(st.UploadID, "a", "identity"); e != nil {
			t.Fatal(e)
		}
	}
	if len(events) != 3 {
		t.Fatal("commit retry emitted", events)
	}
}
func TestOSSImportCommitHookAndRestartRecovery(t *testing.T) {
	s := uploadStore(t)
	events := make(chan Object, 8)
	s.OnCommit = func(o Object) { events <- o }
	raw := []byte("imported bytes")
	service, e := NewImports(s, ImportDependencies{Download: fakeImportDownload(raw)})
	if e != nil {
		t.Fatal(e)
	}
	request := importReq(raw)
	accepted, e := service.Accept(request, "a")
	if e != nil {
		t.Fatal(e)
	}
	status := awaitImport(t, service, accepted.TaskID, "succeeded")
	service.Stop()
	select {
	case ev := <-events:
		if ev.ID != status.ObjectID {
			t.Fatal(ev)
		}
	case <-time.After(time.Second):
		t.Fatal("missing event")
	}
	service, e = NewImports(s, ImportDependencies{Download: fakeImportDownload(raw)})
	if e != nil {
		t.Fatal(e)
	}
	defer service.Stop()
	if _, e = service.Accept(request, "a"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.committedImport(accepted.TaskID, "a", request.SHA256, request.SizeBytes); e != nil {
		t.Fatal(e)
	}
	select {
	case ev := <-events:
		t.Fatal("recovery emitted", ev)
	default:
	}
	// Invalid staging data must not notify.
	missing := filepath.Join(t.TempDir(), "missing")
	if _, e = s.ImportFile(missing, Metadata{}, "a"); e == nil {
		t.Fatal("missing file accepted")
	}
	select {
	case ev := <-events:
		t.Fatal(ev)
	default:
	}
}
