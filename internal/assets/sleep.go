package assets

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/healthdata"
)

// SleepRoute resolves exactly one date file; no directory walk or global session index.
func SleepRoute(path string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/assets/health-data/"), "/")
	if !strings.HasPrefix(path, "/assets/health-data/") || len(parts) != 3 || !validDate(parts[0]) || parts[1] != "sleep-sessions" || !healthdata.SleepIDPattern.MatchString(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[2], true
}

type SleepResponse struct {
	OK         bool                        `json:"ok"`
	Date       string                      `json:"date"`
	Revision   string                      `json:"revision"`
	Session    healthdata.SleepListSummary `json:"session"`
	Items      []healthdata.SleepSegment   `json:"items"`
	NextCursor *string                     `json:"nextCursor"`
}

func (s *Service) ReadSleep(ctx context.Context, date, id string, values url.Values) (SleepResponse, *Error) {
	var out SleepResponse
	if !validDate(date) || !healthdata.SleepIDPattern.MatchString(id) {
		return out, invalid()
	}
	for k, v := range values {
		if (k != "limit" && k != "cursor" && k != "revision") || len(v) != 1 {
			return out, invalid()
		}
	}
	limit := 100
	if v, ok := values["limit"]; ok {
		if v[0] == "" || strings.Trim(v[0], "0123456789") != "" {
			return out, invalid()
		}
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 || n > 500 {
			return out, invalid()
		}
		limit = n
	}
	c := cursor{V: 1, Kind: "health-data/sleep", Mode: "sleep_segments", ScopeHash: digest(s.profile), BootID: s.boot, FileKey: date, Query: map[string]string{"sessionId": id}}
	revision := values.Get("revision")
	if token := values.Get("cursor"); token != "" {
		var e *Error
		c, e = s.decode(token)
		if e != nil {
			return out, e
		}
		if c.ScopeHash != digest(s.profile) {
			return out, failure(403, "CURSOR_SCOPE_MISMATCH", "Cursor belongs to another profile", "none")
		}
		if c.Kind != "health-data/sleep" || c.Mode != "sleep_segments" || c.FileKey != date || c.Query["sessionId"] != id || revision != "" && revision != c.IndexRevision {
			return out, failure(400, "CURSOR_QUERY_MISMATCH", "Sleep detail query changed", "none")
		}
		revision = c.IndexRevision
	}
	if !healthdata.SleepIDPattern.MatchString(revision) {
		return out, invalid()
	}
	if ctx.Err() != nil {
		return out, readFailed()
	}
	budget := ScanLimit
	raw, size, e := s.healthBytes(dayFile{date, date + ".jsonl"}, &budget)
	if e != nil {
		return out, e
	}
	if size > ScanLimit {
		return out, tooLarge()
	}
	actual := digest(string(raw))
	if revision != actual {
		return out, expired()
	}
	day, _, err := healthdata.ReadSnapshot(raw, date)
	if err != nil {
		return out, readFailed()
	}
	var found *healthdata.SleepSession
	for i := range day.SleepSessions {
		if day.SleepSessions[i].ID == id {
			found = &day.SleepSessions[i]
			break
		}
	}
	if found == nil {
		return out, failure(404, "SLEEP_SESSION_NOT_FOUND", "Sleep session is no longer available", "refresh")
	}
	if c.NextIndex < 0 || c.NextIndex >= len(found.Segments) {
		return out, expired()
	}
	out = SleepResponse{OK: true, Date: date, Revision: actual, Session: found.Summary(), Items: []healthdata.SleepSegment{}}
	base, _ := json.Marshal(out)
	sizeUsed := len(base) + envelopeReserve
	index := c.NextIndex
	for index < len(found.Segments) && len(out.Items) < limit {
		if ctx.Err() != nil {
			return SleepResponse{}, readFailed()
		}
		encoded, _ := json.Marshal(found.Segments[index])
		if sizeUsed+len(encoded)+1 > ResponseLimit {
			break
		}
		out.Items = append(out.Items, found.Segments[index])
		sizeUsed += len(encoded) + 1
		index++
	}
	if len(out.Items) == 0 {
		return SleepResponse{}, tooLarge()
	}
	if index < len(found.Segments) {
		c.NextIndex = index
		c.IndexRevision = actual
		c.Exp = s.now().Add(time.Hour).Unix()
		token := s.sign(c)
		out.NextCursor = &token
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > ResponseLimit {
		return SleepResponse{}, readFailed()
	}
	return out, nil
}
