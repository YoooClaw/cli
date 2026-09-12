package aitodo

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var isoInstant = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$`)

func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, e := n.Int64()
		return i, e == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) && n == math.Trunc(n) && math.Abs(n) <= 8640000000000000 {
			return int64(n), true
		}
	}
	return 0, false
}
func sameNumber(a, b any) bool { x, ok := integer(a); y, ok2 := integer(b); return ok && ok2 && x == y }
func validMillis(v any) bool {
	if v == nil {
		return true
	}
	n, ok := integer(v)
	return ok && n >= -8640000000000000 && n <= 8640000000000000
}
func isoMillis(s string, allDay bool) (int64, error) {
	if allDay && isoDate.MatchString(s) {
		d, e := time.Parse("2006-01-02", s)
		if e != nil {
			return 0, invalid("Invalid calendar date")
		}
		return d.UnixMilli(), nil
	}
	if !isoInstant.MatchString(s) || strings.HasSuffix(s, "-00:00") {
		return 0, invalid("Use ISO START time with an explicit timezone offset; all-day items use YYYY-MM-DD; unset time uses JSON null")
	}
	// Go accepts out-of-range zone components; validate before parsing.
	if !strings.HasSuffix(s, "Z") {
		zone := s[len(s)-6:]
		h, _ := strconv.Atoi(zone[1:3])
		m, _ := strconv.Atoi(zone[4:])
		if h > 23 || m > 59 {
			return 0, invalid("Invalid timezone offset")
		}
	}
	// RFC3339 requires seconds; the tool contract also accepts minute precision.
	zoneStart := len(s) - 1
	if !strings.HasSuffix(s, "Z") {
		zoneStart = len(s) - 6
	}
	if zoneStart == 16 {
		s = s[:16] + ":00" + s[16:]
	}
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return 0, invalid("Invalid ISO start time")
	}
	if allDay {
		t, e = time.Parse("2006-01-02", s[:10])
		if e != nil {
			return 0, invalid("Invalid calendar date")
		}
	}
	return t.UnixMilli(), nil
}

var fields = map[string][]string{
	"list": {"isDone", "startTime", "endTime", "keyword", "pageNo", "pageSize"}, "get": {"todoId"},
	"create": {"title", "dueAt", "isFullDay"}, "update": {"todoId", "title", "dueAt", "isFullDay", "isDone"}, "delete": {"todoIds", "confirmed"},
}

func Validate(action string, raw Fields) (Fields, error) {
	allowed, ok := fields[action]
	if !ok {
		return nil, invalid("Unknown todo action")
	}
	if raw == nil {
		return nil, invalid("Expected a JSON object")
	}
	p := Fields{}
	for k, v := range raw {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
			}
		}
		if !found {
			return nil, invalid("Unsupported field: " + k)
		}
		p[k] = v
	}
	if action == "create" {
		if !rawHas(p, "dueAt") {
			p["dueAt"] = nil
		}
	}
	if (action == "create" || (action == "update" && rawHas(p, "dueAt"))) && !rawHas(p, "isFullDay") {
		date, ok := p["dueAt"].(string)
		p["isFullDay"] = ok && isoDate.MatchString(date)
	}
	for _, k := range []string{"dueAt", "startTime", "endTime"} {
		if s, ok := p[k].(string); ok {
			ms, e := isoMillis(s, k == "dueAt" && p["isFullDay"] == true)
			if e != nil {
				return nil, e
			}
			p[k] = ms
			if k == "dueAt" && action == "update" {
				if _, exists := p["isFullDay"]; !exists {
					p["isFullDay"] = false
				}
			}
		}
		if v, exists := p[k]; exists && !validMillis(v) {
			return nil, invalid(k + " must be ISO time, Unix milliseconds or null")
		}
	}
	if action == "get" || action == "update" {
		if !validID(p["todoId"]) {
			return nil, invalid("todoId must be a nonempty string returned by the service")
		}
	}
	if action == "create" || rawHas(p, "title") {
		s, ok := p["title"].(string)
		s = strings.TrimSpace(s)
		if !ok || s == "" || utf8.RuneCountInString(s) > 30 {
			return nil, invalid("title must contain 1–30 characters")
		}
		p["title"] = s
	}
	if v, exists := p["isFullDay"]; exists {
		if _, ok := v.(bool); !ok {
			return nil, invalid("isFullDay must be boolean")
		}
	}
	if v, exists := p["isDone"]; exists {
		if _, ok := v.(bool); !ok && !(action == "list" && v == nil) {
			return nil, invalid("isDone must be boolean (or null for all statuses in list)")
		}
	}
	if p["isFullDay"] == true {
		ms, ok := integer(p["dueAt"])
		if !ok || ms%86400000 != 0 {
			return nil, invalid("All-day dueAt must encode its business date at UTC midnight")
		}
	}
	if action == "update" {
		if len(p) <= 1 {
			return nil, invalid("At least one update field is required")
		}
		if rawHas(p, "isFullDay") && !rawHas(p, "dueAt") {
			return nil, invalid("Send dueAt together with isFullDay")
		}
	}
	if action == "list" {
		if p["startTime"] != nil && p["endTime"] != nil {
			start, _ := integer(p["startTime"])
			end, _ := integer(p["endTime"])
			if end <= start {
				return nil, invalid("endTime must be after startTime")
			}
		}
		if v := p["keyword"]; v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, invalid("keyword must be string or null")
			}
			p["keyword"] = strings.TrimSpace(s)
		}
		if !rawHas(p, "isDone") {
			p["isDone"] = false
		}
		if !rawHas(p, "pageNo") {
			p["pageNo"] = 1
		}
		if p["pageSize"] == nil {
			p["pageSize"] = 20
		}
		for _, k := range []string{"pageNo", "pageSize"} {
			n, ok := integer(p[k])
			if !ok || n < 1 || n > 9007199254740991 {
				return nil, invalid(k + " must be a positive safe integer")
			}
			p[k] = n
		}
	}
	if action == "delete" {
		if p["confirmed"] != true {
			return nil, invalid("Confirm the specific deletion before invoking delete")
		}
		var ids []string
		switch v := p["todoIds"].(type) {
		case []string:
			ids = v
		case []any:
			for _, id := range v {
				if !validID(id) {
					return nil, invalid("todoIds must contain nonempty string IDs")
				}
				ids = append(ids, id.(string))
			}
		}
		if len(ids) == 0 {
			return nil, invalid("todoIds must not be empty")
		}
		seen := map[string]bool{}
		out := []string{}
		for _, id := range ids {
			if !validID(id) {
				return nil, invalid("Invalid todoId")
			}
			if !seen[id] {
				out = append(out, id)
				seen[id] = true
			}
		}
		p["todoIds"] = out
	}
	return p, nil
}
func rawHas(p Fields, k string) bool { _, ok := p[k]; return ok }
