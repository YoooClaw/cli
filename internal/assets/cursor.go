package assets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type skipLine struct {
	Anchor   int64 `json:"anchor"`
	Scanned  int64 `json:"scanned"`
	EndHasLF bool  `json:"endHasLf,omitempty"`
}
type cursor struct {
	V             int               `json:"v"`
	Kind          string            `json:"kind"`
	Mode          string            `json:"mode"`
	ScopeHash     string            `json:"scopeHash"`
	BootID        string            `json:"bootId"`
	Exp           int64             `json:"exp"`
	Query         map[string]string `json:"query"`
	FileKey       string            `json:"fileKey,omitempty"`
	FileID        string            `json:"fileId,omitempty"`
	FileVersion   string            `json:"fileVersion,omitempty"`
	SnapshotEnd   int64             `json:"snapshotEnd,omitempty"`
	BeforeByte    int64             `json:"beforeByte,omitempty"`
	AfterByte     int64             `json:"afterByte,omitempty"`
	SkipLine      *skipLine         `json:"skipLine,omitempty"`
	BeforeDate    string            `json:"beforeDate,omitempty"`
	IndexRevision string            `json:"indexRevision,omitempty"`
	NextIndex     int               `json:"nextIndex,omitempty"`
}
type request struct {
	cursor
	limit  int
	resume bool
}

func (s *Service) sign(c cursor) string {
	raw, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Service) decode(token string) (cursor, *Error) {
	bad := failure(400, "CURSOR_INVALID", "Invalid cursor", "none")
	var c cursor
	if len(token) > CursorLimit {
		return c, bad
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, bad
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || json.Unmarshal(raw, &c) != nil || c.V != 1 {
		return c, bad
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || len(sig) != sha256.Size {
		return c, bad
	}
	// A restart discards the signing key. An unknown boot is only an expiration
	// hint, never authorization; no file or scope is accessed on this path.
	if c.BootID != s.boot {
		return c, expired()
	}
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte(parts[0]))
	if !hmac.Equal(m.Sum(nil), sig) {
		return c, bad
	}
	if c.Exp <= s.now().Unix() {
		return c, expired()
	}
	return c, nil
}

func (s *Service) request(kind string, values url.Values) (request, *Error) {
	bounds, ok := limits[kind]
	if !ok {
		return request{}, invalid()
	}
	for k, v := range values {
		allowed := k == "limit" || k == "cursor" || kind == "calendars" && (k == "anchorDate" || k == "direction" || k == "timeZone")
		if !allowed || len(v) != 1 {
			return request{}, invalid()
		}
	}
	n := bounds[0]
	if v, exists := values["limit"]; exists {
		if v[0] == "" || strings.Trim(v[0], "0123456789") != "" {
			return request{}, invalid()
		}
		var e error
		n, e = strconv.Atoi(v[0])
		if e != nil || n < 1 || n > bounds[1] {
			return request{}, invalid()
		}
	}
	r := request{limit: n}
	r.cursor = cursor{V: 1, Kind: kind, ScopeHash: digest(s.profile), BootID: s.boot, Query: map[string]string{}}
	if token := values.Get("cursor"); token != "" {
		c, e := s.decode(token)
		if e != nil {
			return r, e
		}
		if c.Kind != kind {
			return r, failure(400, "CURSOR_QUERY_MISMATCH", "Cursor category does not match", "none")
		}
		if c.ScopeHash != r.ScopeHash {
			return r, failure(403, "CURSOR_SCOPE_MISMATCH", "Cursor belongs to another profile", "none")
		}
		r.cursor = c
		r.resume = true
	}
	if kind == "calendars" {
		if r.resume {
			for _, k := range []string{"direction", "anchorDate", "timeZone"} {
				if v, exists := values[k]; exists && v[0] != r.Query[k] {
					return r, failure(400, "CURSOR_QUERY_MISMATCH", "Calendar query changed; start a new page", "none")
				}
			}
		} else {
			direction := values.Get("direction")
			if direction == "" {
				direction = "forward"
			}
			if direction != "forward" && direction != "backward" {
				return r, invalid()
			}
			tz := values.Get("timeZone")
			if tz == "" {
				tz = time.Local.String()
				if _, e := time.LoadLocation(tz); e != nil || tz == "Local" {
					tz = "UTC"
				}
			}
			zone, e := time.LoadLocation(tz)
			if e != nil || tz == "Local" {
				return r, invalid()
			}
			anchor := values.Get("anchorDate")
			if anchor == "" {
				anchor = s.now().In(zone).Format(time.DateOnly)
			}
			if !validDate(anchor) {
				return r, invalid()
			}
			r.Query = map[string]string{"direction": direction, "timeZone": tz, "anchorDate": anchor}
		}
	}
	return r, nil
}

func validDate(s string) bool {
	d, e := time.Parse(time.DateOnly, s)
	return e == nil && d.Format(time.DateOnly) == s
}
func (c cursor) forward() bool { return c.Query["direction"] == "forward" }
func (c cursor) position() int64 {
	if c.forward() {
		return c.AfterByte
	}
	return c.BeforeByte
}
func (c *cursor) setPosition(n int64) {
	if c.forward() {
		c.AfterByte = n
	} else {
		c.BeforeByte = n
	}
}
