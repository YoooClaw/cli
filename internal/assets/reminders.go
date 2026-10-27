package assets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/reminders"
)

type ReminderResponse struct {
	Response
	Revision string                           `json:"revision"`
	Sources  map[string]reminders.SourceState `json:"sources"`
}

func ReminderRoute(path string) (string, bool) {
	if path == "/assets/reminders" {
		return "", true
	}
	if !strings.HasPrefix(path, "/assets/reminders/") {
		return "", false
	}
	id := strings.TrimPrefix(path, "/assets/reminders/")
	if len(id) != 64 || strings.ToLower(id) != id {
		return "", false
	}
	_, err := hex.DecodeString(id)
	return id, err == nil
}

var reminderFilters = []string{"status", "sourceId", "listId", "dueFrom", "dueTo", "completedFrom", "completedTo"}

func (s *Service) reminderRequest(values url.Values) (request, *Error) {
	r := request{limit: 50, cursor: cursor{V: 1, Kind: "reminders", Mode: "reminders_index", ScopeHash: digest(s.profile), BootID: s.boot, Query: map[string]string{"status": "incomplete"}}}
	for key, v := range values {
		allowed := key == "limit" || key == "cursor"
		for _, name := range reminderFilters {
			allowed = allowed || name == key
		}
		if !allowed || len(v) != 1 || v[0] == "" || len(v[0]) > CursorLimit {
			return r, invalid()
		}
	}
	if value := values.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 || strings.Trim(value, "0123456789") != "" {
			return r, invalid()
		}
		r.limit = n
	}
	if token := values.Get("cursor"); token != "" {
		c, err := s.decode(token)
		if err != nil {
			return r, err
		}
		if c.Kind != r.Kind || c.Mode != r.Mode {
			return r, failure(400, "CURSOR_QUERY_MISMATCH", "Cursor query does not match", "none")
		}
		if c.ScopeHash != r.ScopeHash {
			return r, failure(403, "CURSOR_SCOPE_MISMATCH", "Cursor belongs to another profile", "none")
		}
		r.cursor, r.resume = c, true
	}
	for _, key := range reminderFilters {
		if value := values.Get(key); value != "" {
			if r.resume && r.Query[key] != value {
				return r, failure(400, "CURSOR_QUERY_MISMATCH", "Filters changed; refresh the list", "none")
			}
			r.Query[key] = value
		}
	}
	if r.Query["status"] != "incomplete" && r.Query["status"] != "completed" && r.Query["status"] != "all" {
		return r, invalid()
	}
	for _, key := range []string{"dueFrom", "dueTo", "completedFrom", "completedTo"} {
		if v := r.Query[key]; v != "" && !reminders.ValidDate(v) {
			return r, invalid()
		}
	}
	for _, prefix := range []string{"due", "completed"} {
		if from, to := r.Query[prefix+"From"], r.Query[prefix+"To"]; from != "" && to != "" && from > to {
			return r, invalid()
		}
	}
	if len(r.Query["sourceId"]) > 256 || len(r.Query["listId"]) > 1024 {
		return r, invalid()
	}
	// JSON escaping and base64 can exceed the decoder limit even when raw IDs fit.
	// Reserve the largest position so every continuation for this query is usable.
	probe := r.cursor
	probe.IndexRevision = strings.Repeat("0", 64)
	probe.NextIndex = int(^uint(0) >> 1)
	probe.Exp = s.now().Add(time.Hour).Unix()
	if len(s.sign(probe)) > CursorLimit {
		return r, failure(400, "INVALID_PARAMS", "Filters exceed the pagination cursor size limit", "none")
	}
	return r, nil
}

func reminderMatches(e reminders.Entry, q map[string]string) bool {
	if q["sourceId"] != "" && e.SourceID != q["sourceId"] || q["listId"] != "" && e.ListID != q["listId"] {
		return false
	}
	if q["status"] == "completed" && !e.IsCompleted || q["status"] == "incomplete" && e.IsCompleted {
		return false
	}
	date := ""
	if e.Due != nil {
		date = e.Due.Date
	}
	if !reminderDateMatches(date, q["dueFrom"], q["dueTo"]) {
		return false
	}
	date = ""
	if e.IsCompleted && e.CompletedAt != nil && len(*e.CompletedAt) >= 10 {
		date = (*e.CompletedAt)[:10]
	}
	return reminderDateMatches(date, q["completedFrom"], q["completedTo"])
}

func reminderDateMatches(date, from, to string) bool {
	return (from == "" && to == "") || date != "" && (from == "" || date >= from) && (to == "" || date <= to)
}

func reminderLess(a, b reminders.Entry) bool {
	if a.IsCompleted != b.IsCompleted {
		return !a.IsCompleted
	}
	if a.IsCompleted {
		if (a.CompletedAt == nil) != (b.CompletedAt == nil) {
			return a.CompletedAt != nil
		}
		if a.CompletedAt != nil {
			x, _ := time.Parse(time.RFC3339Nano, *a.CompletedAt)
			y, _ := time.Parse(time.RFC3339Nano, *b.CompletedAt)
			if !x.Equal(y) {
				return x.After(y)
			}
		}
	} else {
		if (a.Due == nil) != (b.Due == nil) {
			return a.Due != nil
		}
		if a.Due != nil {
			if a.Due.Date != b.Due.Date {
				return a.Due.Date < b.Due.Date
			}
			if (a.Due.Time == nil) != (b.Due.Time == nil) {
				return a.Due.Time == nil
			}
			if a.Due.Time != nil && *a.Due.Time != *b.Due.Time {
				return *a.Due.Time < *b.Due.Time
			}
		}
	}
	return a.RecordID < b.RecordID
}

func reminderItem(record any, id string) Item {
	raw, _ := json.Marshal(record)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	delete(data, "file")
	return Item{RecordID: id, Status: "ok", Data: data, TruncatedFields: []string{}}
}

func (s *Service) ReadReminders(ctx context.Context, store *reminders.Storage, id string, values url.Values) (ReminderResponse, *Error) {
	response := ReminderResponse{}
	var r request
	var e *Error
	if id == "" {
		r, e = s.reminderRequest(values)
		if e != nil {
			return response, e
		}
	} else {
		for key, value := range values {
			if key != "revision" || len(value) != 1 || len(value[0]) != 64 {
				return response, invalid()
			}
		}
	}
	err := store.View(func(idx reminders.Index, state reminders.State, detail func(string) (reminders.Record, error)) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.resume && r.IndexRevision != idx.Revision || id != "" && values.Get("revision") != "" && values.Get("revision") != idx.Revision {
			return expired()
		}
		p := newPage()
		response.Revision, response.Sources = idx.Revision, state.Sources
		if source := r.Query["sourceId"]; source != "" {
			response.Sources = map[string]reminders.SourceState{}
			if value, ok := state.Sources[source]; ok {
				response.Sources[source] = value
			}
		}
		meta, _ := json.Marshal(response.Sources)
		p.bytes += len(meta) + len(idx.Revision)
		if id != "" {
			record, err := detail(id)
			if errors.Is(err, reminders.ErrNotFound) {
				return failure(404, "REMINDER_NOT_FOUND", "Reminder is no longer in this mirror", "refresh")
			}
			if err != nil {
				return err
			}
			if !p.add(reminderItem(record, id)) {
				return tooLarge()
			}
		} else {
			entries := []reminders.Entry{}
			for _, entry := range idx.Items {
				if reminderMatches(entry, r.Query) {
					entries = append(entries, entry)
				}
			}
			sort.Slice(entries, func(i, j int) bool { return reminderLess(entries[i], entries[j]) })
			if r.NextIndex < 0 || r.NextIndex > len(entries) {
				return expired()
			}
			at := r.NextIndex
			for at < len(entries) && len(p.Items) < r.limit {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !p.add(reminderItem(entries[at], entries[at].RecordID)) {
					break
				}
				at++
			}
			if at < len(entries) {
				if len(p.Items) == 0 {
					return tooLarge()
				}
				r.IndexRevision, r.NextIndex = idx.Revision, at
				s.next(p, r.cursor)
			}
		}
		response.Response = p.Response
		return nil
	})
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) {
			return response, ae
		}
		return response, readFailed()
	}
	raw, err := json.Marshal(response)
	if err != nil || len(raw) > ResponseLimit {
		return response, tooLarge()
	}
	return response, nil
}
