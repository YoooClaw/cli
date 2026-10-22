package assets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/reminders"
)

func reminderPtr(s string) *string { return &s }

func reminderFixture(t *testing.T) (*Service, *reminders.Storage, reminders.Payload) {
	t.Helper()
	store := reminders.NewStorage(t.TempDir())
	p := reminders.Payload{SchemaVersion: 1, SourceID: "phone", CollectedAt: "2026-10-01T09:00:00Z", Items: []reminders.Item{}}
	for i := 0; i < 125; i++ {
		item := reminders.Item{Summary: reminders.Summary{ID: fmt.Sprint(i), Title: "same title", ListID: "personal", ListName: "Personal"}, Notes: reminderPtr("secret")}
		if i < 100 {
			item.Due = &reminders.Due{Date: "2026-10-02"}
		}
		if i < 30 {
			item.Due.Time = reminderPtr("12:00:00")
		}
		if i >= 110 {
			item.IsCompleted = true
			if i < 120 {
				item.CompletedAt = reminderPtr("2026-10-01T08:00:00Z")
			}
		}
		p.Items = append(p.Items, item)
	}
	if _, err := store.Replace(p); err != nil {
		t.Fatal(err)
	}
	return New("profile", nil), store, p
}

func reminderRead(t *testing.T, s *Service, store *reminders.Storage, id string, q url.Values) ReminderResponse {
	t.Helper()
	r, e := s.ReadReminders(context.Background(), store, id, q)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestReminderPaginationFiltersAndDetail(t *testing.T) {
	s, store, _ := reminderFixture(t)
	q := url.Values{"status": {"all"}, "limit": {"17"}}
	seen := map[string]bool{}
	var first ReminderResponse
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("cursor did not end")
		}
		page := reminderRead(t, s, store, "", q)
		if pages == 0 {
			first = page
		}
		if len(page.Items) > 17 {
			t.Fatal("limit ignored")
		}
		for _, item := range page.Items {
			if seen[item.RecordID] {
				t.Fatal("duplicate record")
			}
			seen[item.RecordID] = true
			for _, key := range []string{"notes", "url", "createdAt", "modifiedAt", "file"} {
				if _, ok := item.Data[key]; ok {
					t.Fatal("list leaked detail/path", key)
				}
			}
		}
		if page.NextCursor == nil {
			break
		}
		q = url.Values{"cursor": {*page.NextCursor}, "limit": {"17"}}
	}
	if len(seen) != 125 {
		t.Fatal(len(seen))
	}
	data := first.Items[0].Data
	if data["isCompleted"] != false || data["due"].(map[string]any)["time"] != nil {
		t.Fatal("wrong due/status order")
	}
	detail := reminderRead(t, s, store, first.Items[0].RecordID, url.Values{"revision": {first.Revision}})
	if len(detail.Items) != 1 || detail.Items[0].Data["notes"] != "secret" {
		t.Fatal("detail incomplete")
	}
	page := reminderRead(t, s, store, "", url.Values{"status": {"completed"}, "completedFrom": {"2026-10-01"}, "limit": {"100"}})
	if len(page.Items) != 10 {
		t.Fatal("undated completion matched date range")
	}
	page = reminderRead(t, s, store, "", url.Values{"dueTo": {"2026-10-01"}})
	if len(page.Items) != 0 {
		t.Fatal("undated or later reminder matched due range")
	}
	page = reminderRead(t, s, store, "", url.Values{"sourceId": {"missing"}})
	if len(page.Items) != 0 || len(page.Sources) != 0 {
		t.Fatal("source filter")
	}
}

func TestReminderCursorRevisionQueryAndProfile(t *testing.T) {
	s, store, p := reminderFixture(t)
	first := reminderRead(t, s, store, "", url.Values{"limit": {"1"}})
	q := url.Values{"cursor": {*first.NextCursor}}
	p.CollectedAt = "2026-10-01T10:00:00Z"
	if _, err := store.Replace(p); err != nil {
		t.Fatal(err)
	}
	reminderRead(t, s, store, "", q)
	q.Set("status", "completed")
	if _, e := s.ReadReminders(context.Background(), store, "", q); e == nil || e.Code != "CURSOR_QUERY_MISMATCH" {
		t.Fatal(e)
	}
	q.Del("status")
	other := New("other", nil)
	other.key, other.boot = s.key, s.boot
	if _, e := other.ReadReminders(context.Background(), store, "", q); e == nil || e.Status != 403 {
		t.Fatal(e)
	}
	p.Items[0].Notes = reminderPtr("note-only change")
	p.CollectedAt = "2026-10-01T11:00:00Z"
	if _, err := store.Replace(p); err != nil {
		t.Fatal(err)
	}
	if _, e := s.ReadReminders(context.Background(), store, "", q); e == nil || e.Status != 410 {
		t.Fatal(e)
	}
	if _, e := s.ReadReminders(context.Background(), store, first.Items[0].RecordID, url.Values{"revision": {first.Revision}}); e == nil || e.Status != 410 {
		t.Fatal(e)
	}
	if _, e := s.ReadReminders(context.Background(), store, strings.Repeat("f", 64), nil); e == nil || e.Status != 404 {
		t.Fatal(e)
	}
}

func TestReminderResponseBudgetAndIndexOnlyList(t *testing.T) {
	dir := t.TempDir()
	store := reminders.NewStorage(dir)
	p := reminders.Payload{SchemaVersion: 1, SourceID: "phone", CollectedAt: "2026-10-01T09:00:00Z", Items: []reminders.Item{}}
	for i := 0; i < 10; i++ {
		p.Items = append(p.Items, reminders.Item{Summary: reminders.Summary{ID: fmt.Sprint(i), Title: strings.Repeat("界", 16000), ListID: "l", ListName: "L"}})
	}
	if _, err := store.Replace(p); err != nil {
		t.Fatal(err)
	}
	// Lists must be served from index alone, even when the detail partition is unavailable.
	if err := os.Rename(filepath.Join(dir, "active.jsonl"), filepath.Join(dir, "active.saved")); err != nil {
		t.Fatal(err)
	}
	s := New("profile", nil)
	page := reminderRead(t, s, store, "", url.Values{"limit": {"100"}})
	raw, _ := json.Marshal(page)
	if len(raw) > ResponseLimit || len(page.Items) == 0 || len(page.Items) >= 10 || page.NextCursor == nil {
		t.Fatal("byte limit did not paginate")
	}
	if _, e := s.ReadReminders(context.Background(), store, page.Items[0].RecordID, nil); e == nil || e.Status != 500 {
		t.Fatal("missing indexed detail should fail", e)
	}
}

func TestReminderInvalidQueries(t *testing.T) {
	s, store, _ := reminderFixture(t)
	for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=2", "status=done", "dueFrom=2026-02-30", "dueFrom=2026-10-02&dueTo=2026-10-01", "cursor=bad", "unknown=1"} {
		q, _ := url.ParseQuery(query)
		if _, e := s.ReadReminders(context.Background(), store, "", q); e == nil || e.Status != 400 {
			t.Fatal(query, e)
		}
	}
}

func TestReminderCursorBudgetIncludesJSONEscaping(t *testing.T) {
	for _, listID := range []string{strings.Repeat("<", 1024), strings.Repeat("x", 1024)} {
		t.Run(listID[:1], func(t *testing.T) {
			s, store, p := reminderFixture(t)
			for i := range p.Items {
				p.Items[i].ListID = listID
			}
			p.CollectedAt = "2026-10-01T10:00:00Z"
			if _, err := store.Replace(p); err != nil {
				t.Fatal(err)
			}
			q := url.Values{"listId": {listID}, "limit": {"1"}}
			page, e := s.ReadReminders(context.Background(), store, "", q)
			if listID[0] == '<' {
				if e == nil || e.Status != 400 || e.Code != "INVALID_PARAMS" {
					t.Fatal("oversize continuation was not rejected on the first page", e)
				}
				return
			}
			if e != nil || page.NextCursor == nil || len(*page.NextCursor) > CursorLimit {
				t.Fatal("valid long filter cannot paginate", e)
			}
			next := reminderRead(t, s, store, "", url.Values{"cursor": {*page.NextCursor}, "limit": {"1"}})
			if len(next.Items) != 1 || next.Items[0].RecordID == page.Items[0].RecordID {
				t.Fatal("cursor did not advance")
			}
		})
	}
}
