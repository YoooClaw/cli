package cli

import "testing"

// fixture 里 mdn 的 clientLabel 是 webext，internal 没有 label（显示为 legacy）。
func TestWebListFiltersByClient(t *testing.T) {
	_, mdn, internal := writeWebFixture(t)

	out, code := execCLI(t, "synced-web-page", "list", "--client", "webext")
	if code != 0 {
		t.Fatalf("client-filtered list failed: %s", out)
	}
	result := decode(t, out)
	pages := result["pages"].([]any)
	if result["total"] != float64(1) || pages[0].(map[string]any)["urlHash"] != mdn.URLHash {
		t.Fatalf("--client webext = %+v, want only mdn", result)
	}

	out, _ = execCLI(t, "synced-web-page", "list", "--client", "legacy")
	result = decode(t, out)
	pages = result["pages"].([]any)
	if result["total"] != float64(1) || pages[0].(map[string]any)["urlHash"] != internal.URLHash {
		t.Fatalf("--client legacy = %+v, want only the unlabeled page", result)
	}

	out, _ = execCLI(t, "synced-web-page", "list", "--client", "all")
	if decode(t, out)["total"] != float64(2) {
		t.Fatalf("--client all should not filter: %s", out)
	}
}

func TestWebSearchFiltersByClient(t *testing.T) {
	writeWebFixture(t)
	out, code := execCLI(t, "synced-web-page", "search", "guide", "--client", "webext")
	if code != 0 {
		t.Fatalf("client-filtered search failed: %s", out)
	}
	if total := decode(t, out)["total"]; total != float64(0) {
		t.Fatalf("search --client webext total = %v, want 0 (命中的是 legacy 那篇)", total)
	}
	out, _ = execCLI(t, "synced-web-page", "search", "guide", "--client", "legacy")
	if total := decode(t, out)["total"]; total != float64(1) {
		t.Fatalf("search --client legacy total = %v, want 1", total)
	}
}
