package assets

import (
	"bytes"
	"encoding/json"
	"math"
	"time"
	"unicode/utf8"
)

func object(raw []byte) (map[string]any, bool) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, false
	}
	var obj map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&obj) != nil || obj == nil {
		return nil, false
	}
	return obj, true
}
func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func optionalString(m map[string]any, k string) any {
	v, ok := m[k].(string)
	if !ok || v == "" {
		return nil
	}
	return v
}
func stamp(v any) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if _, e := time.Parse(time.RFC3339Nano, s); e != nil {
		return nil
	}
	return s
}
func numeric(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	f, e := n.Float64()
	if e != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return n
}
func boolean(v any) any {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return b
}
func nullableTimes(v any) any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	for _, item := range arr {
		if stamp(item) == nil {
			return nil
		}
	}
	return arr
}
func item(id, date string, data map[string]any) Item {
	var day *string
	if date != "" {
		day = &date
	}
	return Item{RecordID: id, StorageDate: day, Status: "ok", Data: data, TruncatedFields: []string{}}
}
func oversizedItem(id, date string, size int64) Item {
	it := item(id, date, nil)
	it.Status = "oversized"
	it.SourceBytes = size
	return it
}

func project(kind string, m map[string]any, id, date string) (Item, bool) {
	d := map[string]any{}
	switch kind {
	case "notifications":
		if str(m, "appName") == "" || stamp(m["timestamp"]) == nil {
			return Item{}, false
		}
		app := str(m, "appDisplayName")
		if app == "" {
			app = str(m, "appName")
		}
		d = map[string]any{"appName": str(m, "appName"), "appDisplayName": app, "title": str(m, "title"),
			"content": str(m, "content"), "timestamp": stamp(m["timestamp"])}
		for _, key := range []string{"senderName", "conversationType", "conversationName", "clientLabel"} {
			d[key] = optionalString(m, key)
		}
	case "locations":
		for key, bound := range map[string]float64{"longitude": 180, "latitude": 90} {
			n := numeric(m[key])
			if n == nil {
				return Item{}, false
			}
			f, _ := n.(json.Number).Float64()
			if f < -bound || f > bound {
				return Item{}, false
			}
			d[key] = n
		}
		for _, key := range []string{"id", "coordinateSystem", "poi", "address", "provider", "deviceType"} {
			d[key] = optionalString(m, key)
		}
		d["timestamp"] = stamp(m["timestamp"])
		d["accuracyMeters"] = numeric(m["accuracyMeters"])
	case "calendars":
		start, end := stamp(m["startAt"]), stamp(m["endAt"])
		tz := str(m, "timeZone")
		if start == nil || end == nil || tz == "" || tz == "Local" {
			return Item{}, false
		}
		zone, e := time.LoadLocation(tz)
		if e != nil {
			return Item{}, false
		}
		st, _ := time.Parse(time.RFC3339Nano, start.(string))
		en, _ := time.Parse(time.RFC3339Nano, end.(string))
		if en.Before(st) {
			return Item{}, false
		}
		for _, at := range []time.Time{st, en} {
			_, actual := at.Zone()
			_, expected := at.In(zone).Zone()
			if actual != expected {
				return Item{}, false
			}
		}
		d = map[string]any{"title": str(m, "title"), "startAt": start, "endAt": end, "timeZone": tz,
			"isAllDay": boolean(m["isAllDay"]), "reminderTimes": nullableTimes(m["reminderTimes"])}
		for _, key := range []string{"id", "calendarName", "location", "description"} {
			d[key] = optionalString(m, key)
		}
	}
	it := item(id, date, d)
	caps := map[string]int{"title": 512, "appName": 512, "appDisplayName": 512, "senderName": 512, "conversationName": 512,
		"calendarName": 512, "content": 8192, "description": 8192, "address": 1024, "poi": 1024, "location": 1024, "deviceType": 1024}
	boundItem(&it, caps, ItemLimit)
	return it, true
}

func trimField(it *Item, key string, n int) {
	s, ok := it.Data[key].(string)
	if !ok || utf8.RuneCountInString(s) <= n {
		return
	}
	it.Data[key] = string([]rune(s)[:n])
	for _, old := range it.TruncatedFields {
		if old == key {
			return
		}
	}
	it.TruncatedFields = append(it.TruncatedFields, key)
}
func boundItem(it *Item, caps map[string]int, limit int) {
	// Deterministic field order makes retries byte-for-byte comparable.
	keys := sortedKeys(caps)
	for _, k := range keys {
		trimField(it, k, caps[k])
	}
	for {
		raw, _ := json.Marshal(it)
		if len(raw) <= limit {
			return
		}
		largest := ""
		count := 0
		for _, k := range keys {
			if n := utf8.RuneCountInString(str(it.Data, k)); n > count {
				count = n
				largest = k
			}
		}
		if count == 0 {
			it.Status = "oversized"
			it.Data = nil
			it.TruncatedFields = []string{}
			return
		}
		trimField(it, largest, count/2)
	}
}

func (p *page) badRecord(tail bool) {
	if tail {
		p.warn("INCOMPLETE_TAIL_SKIPPED")
	} else {
		p.warn("INVALID_RECORD_SKIPPED")
	}
}

// Empty lines are deliberately not reported as corrupt records.
func empty(raw []byte) bool { return len(bytes.TrimSpace(raw)) == 0 }
