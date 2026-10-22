package daemon

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func assetBridge(s *server, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAssetsBridgeAllCategoriesAndCrossFileCursor(t *testing.T) {
	s, _ := newTestServer(t, "test-token")
	seedAssetEndpoints(t, s)
	for _, tt := range []struct {
		kind       string
		max, total int
	}{
		{"notifications", 100, 133}, {"locations", 100, 125}, {"calendars", 100, 125},
		{"health-data", 14, 16}, {"web-pages", 50, 60},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			query := url.Values{"limit": {fmt.Sprint(tt.max)}}
			if tt.kind == "calendars" {
				query.Set("anchorDate", "2026-10-22")
			}
			seen := map[string]bool{}
			for page := 0; ; page++ {
				if page > 30 {
					t.Fatal("pagination did not terminate")
				}
				path := "/assets/" + tt.kind + "?" + query.Encode()
				response := assetResponse(t, assetBridge(s, path, `{"method":"GET","body":null}`, "test-token"))
				if len(response.Items) > tt.max {
					t.Fatal("page cap exceeded")
				}
				for _, item := range response.Items {
					if seen[item.RecordID] {
						t.Fatal("duplicate record")
					}
					seen[item.RecordID] = true
				}
				if response.NextCursor == nil {
					break
				}
				query.Set("cursor", *response.NextCursor)
				// A POST-envelope cursor is also valid on the original GET route.
				assetResponse(t, assetHTTP(s, "GET", "/assets/"+tt.kind+"?"+query.Encode(), "test-token"))
			}
			if len(seen) != tt.total {
				t.Fatalf("got %d want %d", len(seen), tt.total)
			}
			query = url.Values{"limit": {fmt.Sprint(tt.max + 1)}}
			if w := assetBridge(s, "/assets/"+tt.kind+"?"+query.Encode(), `{"method":"GET"}`, "test-token"); w.Code != 400 {
				t.Fatalf("limit+1: %d", w.Code)
			}
		})
	}
}

func TestAssetsBridgeRejectsWritesMalformedAndUnauthenticated(t *testing.T) {
	s, _ := newTestServer(t, "test-token")
	path := "/assets/notifications"
	for _, body := range []string{
		``, `{}`, `{"method":"POST"}`, `{"method":"DELETE"}`, `{"method":"GET","body":"{}"}`,
		`{"method":"GET","body":{}}`, `{"method":"GET","unexpected":1}`,
		`{"method":"GET"} {}`, `{"method":"GET","body":null}` + strings.Repeat(" ", 4096),
	} {
		if w := assetBridge(s, path, body, "test-token"); w.Code != 400 {
			t.Fatalf("malformed: %d", w.Code)
		}
	}
	if w := assetBridge(s, path, `{"method":"GET"}`, "wrong"); w.Code != 401 {
		t.Fatal("auth bypass")
	}
	if w := assetHTTP(s, "DELETE", path, "test-token"); w.Code != 405 {
		t.Fatal("write allowed")
	}
}
