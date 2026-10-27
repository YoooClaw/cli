// Package reminders stores the current system Reminders mirror, scoped by source.
package reminders

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Keep IANA validation available in standalone Windows installations.
)

const (
	SchemaVersion = 1
	UploadLimit   = 16 << 20
	RecordLimit   = 64 << 10
	StoreLimit    = 128 << 20
	MaxItems      = 20000
)

var ErrNotFound = errors.New("reminder is not in the current mirror")

type Due struct {
	Date     string  `json:"date"`
	Time     *string `json:"time"`
	TimeZone *string `json:"timeZone"`
}

type Summary struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	ListID      string  `json:"listId"`
	ListName    string  `json:"listName"`
	Due         *Due    `json:"due"`
	IsCompleted bool    `json:"isCompleted"`
	CompletedAt *string `json:"completedAt"`
	Priority    int     `json:"priority"`
	IsRecurring bool    `json:"isRecurring"`
}

type Item struct {
	Summary
	Notes      *string `json:"notes"`
	URL        *string `json:"url"`
	CreatedAt  *string `json:"createdAt"`
	ModifiedAt *string `json:"modifiedAt"`
}

type Payload struct {
	SchemaVersion int    `json:"schemaVersion"`
	SourceID      string `json:"sourceId"`
	CollectedAt   string `json:"collectedAt"`
	Items         []Item `json:"items"`
}

type Record struct {
	Item
	SourceID string `json:"sourceId"`
	RecordID string `json:"recordId"`
}

type Entry struct {
	Summary
	SourceID string `json:"sourceId"`
	RecordID string `json:"recordId"`
	File     string `json:"file"`
}

type Index struct {
	SchemaVersion int     `json:"schemaVersion"`
	Revision      string  `json:"revision"`
	Items         []Entry `json:"items"`
}

type SourceState struct {
	LastCollectedAt string `json:"lastCollectedAt"`
}

type State struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Sources       map[string]SourceState `json:"sources"`
}

type Result struct {
	OK       bool   `json:"ok"`
	Applied  bool   `json:"applied"`
	Ingested int    `json:"ingested"`
	Revision string `json:"revision"`
}

func hash(value any) string {
	raw, _ := json.Marshal(value)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func RecordID(source, id string) string { return hash([]string{source, id}) }

func ValidDate(value string) bool {
	t, err := time.Parse("2006-01-02", value)
	return err == nil && t.Format("2006-01-02") == value
}

func timestamp(value *string) bool {
	if value == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, *value)
	return err == nil
}

func validTimeZone(zone string) bool {
	if zone == "" || zone == "Local" {
		return false
	}
	if _, err := time.LoadLocation(zone); err == nil {
		return true
	}
	// Foundation also emits fixed-offset identifiers such as GMT+0530.
	if len(zone) != 8 || zone[:3] != "GMT" || zone[3] != '+' && zone[3] != '-' {
		return false
	}
	hour, hErr := strconv.Atoi(zone[4:6])
	minute, mErr := strconv.Atoi(zone[6:8])
	return hErr == nil && mErr == nil && hour <= 23 && minute <= 59 && strings.Trim(zone[4:], "0123456789") == ""
}

// Decode rejects an incomplete snapshot before it can replace a source mirror.
// Missing/null items is different from a successfully fetched empty array.
func Decode(raw []byte) (Payload, error) {
	var p Payload
	bad := errors.New("invalid reminders snapshot")
	if len(raw) > UploadLimit {
		return p, errors.New("reminders snapshot exceeds 16 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.SchemaVersion != SchemaVersion ||
		strings.TrimSpace(p.SourceID) == "" || len(p.SourceID) > 256 || !timestamp(&p.CollectedAt) ||
		p.Items == nil || len(p.Items) > MaxItems {
		return p, bad
	}
	var fields struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return p, bad
	}
	seen := map[string]bool{}
	for i, item := range p.Items {
		for _, key := range []string{"id", "title", "listId", "listName", "isCompleted", "priority", "isRecurring"} {
			v := fields.Items[i][key]
			if len(v) == 0 || string(v) == "null" {
				return p, fmt.Errorf("items[%d]: missing %s", i, key)
			}
		}
		if item.ID == "" || len(item.ID) > 1024 || item.ListID == "" || len(item.ListID) > 1024 || seen[item.ID] ||
			item.Priority < 0 || item.Priority > 9 || !timestamp(item.CompletedAt) ||
			!timestamp(item.CreatedAt) || !timestamp(item.ModifiedAt) {
			return p, bad
		}
		seen[item.ID] = true
		if due := item.Due; due != nil {
			if !ValidDate(due.Date) {
				return p, bad
			}
			if due.Time != nil {
				t, err := time.Parse("15:04:05", *due.Time)
				if err != nil || t.Format("15:04:05") != *due.Time {
					return p, bad
				}
			}
			if due.TimeZone != nil {
				if !validTimeZone(*due.TimeZone) {
					return p, bad
				}
			}
		}
		raw, _ := json.Marshal(Record{Item: item, SourceID: p.SourceID, RecordID: RecordID(p.SourceID, item.ID)})
		if len(raw) > RecordLimit {
			return p, errors.New("reminder exceeds 64 KiB; snapshot was not applied")
		}
	}
	return p, nil
}

func partition(r Record) string {
	if !r.IsCompleted {
		return "active.jsonl"
	}
	if r.CompletedAt == nil {
		return "completed/undated.jsonl"
	}
	if !timestamp(r.CompletedAt) {
		return "invalid"
	}
	return "completed/" + (*r.CompletedAt)[:10] + ".jsonl"
}

func validPartition(name string) bool {
	return name == "active.jsonl" || name == "completed/undated.jsonl" ||
		strings.HasPrefix(name, "completed/") && strings.HasSuffix(name, ".jsonl") &&
			ValidDate(strings.TrimSuffix(strings.TrimPrefix(name, "completed/"), ".jsonl"))
}
