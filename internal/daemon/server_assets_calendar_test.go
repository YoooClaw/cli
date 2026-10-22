package daemon

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestAssetsCalendarCrossDatePageGETAndBridge(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, direction := range []string{"forward", "backward"} {
			t.Run(method+"/"+direction, func(t *testing.T) {
				s, _ := newTestServer(t, "test-token")
				row := []byte(`{"id":"event","title":"Meeting","startAt":"2026-10-22T09:00:00+08:00","endAt":"2026-10-22T10:00:00+08:00","timeZone":"Asia/Shanghai"}` + "\n")
				for i, n := range []int{1, 2, 0, 1} {
					assetFile(t, s.ctx.Paths.Calendars, fmt.Sprintf("2026-10-%02d.jsonl", 22+i), bytes.Repeat(row, n))
				}
				request := func(method, path string) *httptest.ResponseRecorder {
					if method == "POST" {
						return assetBridge(s, path, `{"method":"GET","body":null}`, "test-token")
					}
					return assetHTTP(s, method, path, "test-token")
				}
				anchor, lastDate := "2026-10-22", "2026-10-25"
				if direction == "backward" {
					anchor, lastDate = lastDate, anchor
				}
				query := url.Values{"limit": {"3"}, "direction": {direction}, "anchorDate": {anchor}}
				first := assetResponse(t, request(method, "/assets/calendars?"+query.Encode()))
				if len(first.Items) != 3 || first.NextCursor == nil ||
					*first.Items[0].StorageDate != anchor || *first.Items[2].StorageDate != "2026-10-23" {
					t.Fatalf("first page did not fill across dates: %d records", len(first.Items))
				}
				query = url.Values{"limit": {"100"}, "cursor": {*first.NextCursor}}
				path := "/assets/calendars?" + query.Encode()
				last := assetResponse(t, request("GET", path))
				bridgeLast := assetResponse(t, request("POST", path))
				if !reflect.DeepEqual(last, bridgeLast) {
					t.Fatal("GET and bridge consumed the same cursor differently")
				}
				if len(last.Items) != 1 || last.NextCursor != nil || *last.Items[0].StorageDate != lastDate {
					t.Fatal("cross-file continuation lost data or did not terminate")
				}
				seen := map[string]bool{}
				for _, item := range append(first.Items, last.Items...) {
					if seen[item.RecordID] {
						t.Fatal("cross-file continuation duplicated a record")
					}
					seen[item.RecordID] = true
				}
			})
		}
	}
}
