package assets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRequestDefaultsAndLimitsEveryCategory(t *testing.T) {
	s := fixture(t)
	for kind, bounds := range limits {
		t.Run(kind, func(t *testing.T) {
			r, e := s.request(kind, url.Values{})
			if e != nil || r.limit != bounds[0] {
				t.Fatalf("default %+v %v", r, e)
			}
			for _, n := range []string{"1", strconv.Itoa(bounds[1])} {
				if _, e := s.request(kind, url.Values{"limit": {n}}); e != nil {
					t.Fatal(e)
				}
			}
			for _, q := range []url.Values{
				{"limit": {"0"}}, {"limit": {"-1"}}, {"limit": {"1.5"}}, {"limit": {"1e2"}}, {"limit": {"+1"}},
				{"limit": {""}}, {"limit": {"9999999999999999999999"}}, {"limit": {string(marshal(t, bounds[1]+1))}},
				{"limit": {"1", "2"}}, {"cursor": {"", ""}}, {"date": {"2026-10-22"}}, {"path": {"../../secret"}},
				{"scope": {"all"}}, {"clientLabel": {"all"}},
			} {
				if _, e := s.request(kind, q); e == nil || e.Code != "INVALID_PARAMS" {
					t.Fatalf("accepted %v: %v", q, e)
				}
			}
			if _, e := s.request(kind, url.Values{"cursor": {""}}); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestCursorAuthenticationBindingExpiryAndRestart(t *testing.T) {
	s := fixture(t)
	put(t, s, "notifications", "2026-10-22.jsonl", notifications(t, 3))
	p := get(t, s, "notifications", url.Values{"limit": {"1"}})
	token := *p.NextCursor
	// Keep signing context equal to isolate the profile binding from restart checks.
	otherProfile := New("other-profile", s.sources)
	otherProfile.key, otherProfile.boot, otherProfile.now = s.key, s.boot, s.now
	assertError(t, otherProfile, "notifications", url.Values{"cursor": {token}}, "CURSOR_SCOPE_MISMATCH", 403)
	assertError(t, s, "locations", url.Values{"cursor": {token}}, "CURSOR_QUERY_MISMATCH", 400)
	for _, value := range []string{"invalid", strings.Repeat("x", CursorLimit+1), token + "x"} {
		assertError(t, s, "notifications", url.Values{"cursor": {value}}, "CURSOR_INVALID", 400)
	}
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var c cursor
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("cannot decode test cursor")
	}
	c.BeforeByte = 0
	tampered := base64.RawURLEncoding.EncodeToString(marshal(t, c)) + "." + parts[1]
	assertError(t, s, "notifications", url.Values{"cursor": {tampered}}, "CURSOR_INVALID", 400)
	later := s.now().Add(time.Hour)
	s.now = func() time.Time { return later }
	assertError(t, s, "notifications", url.Values{"cursor": {token}}, "CURSOR_EXPIRED", 410)
	restarted := New(s.profile, s.sources)
	assertError(t, restarted, "notifications", url.Values{"cursor": {token}}, "CURSOR_EXPIRED", 410)
}

func TestProfileReadsAndDateSymlinks(t *testing.T) {
	s := fixture(t)
	for _, kind := range []string{"locations", "calendars", "health-data"} {
		p, e := s.Read(context.Background(), kind, url.Values{})
		if e != nil || !p.OK {
			t.Fatalf("profile read failed: %s %v", kind, e)
		}
	}
	outside := filepath.Join(t.TempDir(), "2026-10-22.jsonl")
	if e := os.WriteFile(outside, notifications(t, 1), 0600); e != nil {
		t.Fatal(e)
	}
	dir := s.sources["notifications"].Dir
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(dir, "2026-10-22.jsonl")); e != nil {
		t.Skip(e)
	}
	assertError(t, s, "notifications", url.Values{}, "SOURCE_READ_FAILED", 500)
	for _, kind := range []string{"locations", "health-data", "web-pages"} {
		if e := os.Symlink(filepath.Dir(outside), s.sources[kind].Dir); e != nil {
			t.Fatal(e)
		}
		assertError(t, s, kind, url.Values{}, "SOURCE_READ_FAILED", 500)
	}
}

func TestCalendarQueryInheritanceAndDefaults(t *testing.T) {
	s := fixture(t)
	put(t, s, "calendars", "2026-10-22.jsonl", calendarRows(t, 3))
	first := get(t, s, "calendars", url.Values{"limit": {"1"}, "anchorDate": {"2026-10-22"}, "timeZone": {"Asia/Shanghai"}})
	q := nextValues(first, 2)
	if len(get(t, s, "calendars", q).Items) != 2 {
		t.Fatal("limit could not change")
	}
	for key, value := range map[string]string{"direction": "backward", "anchorDate": "2026-10-21", "timeZone": "UTC"} {
		q := nextValues(first, 1)
		q.Set(key, value)
		assertError(t, s, "calendars", q, "CURSOR_QUERY_MISMATCH", 400)
	}
	for _, q := range []url.Values{{"direction": {"sideways"}}, {"anchorDate": {"2026-02-30"}}, {"timeZone": {"Not/A_Zone"}}, {"timeZone": {"Local"}}} {
		assertError(t, s, "calendars", q, "INVALID_PARAMS", 400)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC) }
	r, e := s.request("calendars", url.Values{"timeZone": {"America/Los_Angeles"}})
	if e != nil || r.Query["anchorDate"] != "2026-10-21" || r.Query["direction"] != "forward" {
		t.Fatalf("timezone default: %+v %v", r, e)
	}
}

func FuzzCursorDecoder(f *testing.F) {
	f.Add("not-a-cursor")
	f.Add("e30.AAAA")
	f.Add("")
	s := New("fuzz", nil)
	f.Fuzz(func(t *testing.T, token string) {
		c, e := s.decode(token)
		if e == nil && (c.V != 1 || c.BootID != s.boot) {
			t.Fatal("accepted invalid cursor")
		}
	})
}
