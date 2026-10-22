package daemon

import (
	"net/url"
	"reflect"
	"testing"
)

func TestAssetsWebIndexOnlyGETAndBridgeContract(t *testing.T) {
	s, _ := newTestServer(t, "test-token")
	// No body files or capture counters are needed. All source labels remain visible.
	assetFile(t, s.ctx.Paths.WebPages, "index.json", []byte(`{"pages":[
		{"urlHash":"b","title":"Original","url":"https://example.com/b?from=clip#part",
		 "canonicalUrl":"https://example.com/b","clientLabel":"phone-a"},
		{"urlHash":"a","title":"Fallback","canonicalUrl":"https://example.com/a",
		 "relativePath":"missing.md","clientLabel":"phone-b"}
	]}`))
	first := assetResponse(t, assetHTTP(s, "GET", "/assets/web-pages?limit=1", "test-token"))
	if len(first.Items) != 1 || first.NextCursor == nil || len(first.Warnings) != 0 ||
		!reflect.DeepEqual(first.Items[0].Data, map[string]any{"title": "Original", "url": "https://example.com/b?from=clip#part"}) {
		t.Fatalf("GET projection: %+v", first)
	}
	path := "/assets/web-pages?" + url.Values{"limit": {"50"}, "cursor": {*first.NextCursor}}.Encode()
	next := assetResponse(t, assetBridge(s, path, `{"method":"GET","body":null}`, "test-token"))
	if len(next.Items) != 1 || next.NextCursor != nil || len(next.Warnings) != 0 ||
		next.Items[0].RecordID == first.Items[0].RecordID ||
		!reflect.DeepEqual(next.Items[0].Data, map[string]any{"title": "Fallback", "url": "https://example.com/a"}) {
		t.Fatalf("bridge continuation: %+v", next)
	}
	retry := assetResponse(t, assetHTTP(s, "GET", path, "test-token"))
	if !reflect.DeepEqual(next, retry) {
		t.Fatal("GET and POST-envelope cursor results differ")
	}
}
