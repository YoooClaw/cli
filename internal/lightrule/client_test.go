package lightrule

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// beanOK 包一层云端 ResponseBean{code:"000000", data}。
func beanOK(data map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"code": "000000", "msg": "success", "data": data})
	return raw
}

type recordedRequest struct {
	Method string
	Path   string
	APIKey string
	Body   map[string]any
}

func recordRequest(t *testing.T, r *http.Request) recordedRequest {
	t.Helper()
	rec := recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), APIKey: r.Header.Get("X-Api-Key-Id")}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rec.Body)
	}
	return rec
}

func TestCreateSendsRuleTextWithAPIKeyHeader(t *testing.T) {
	var got recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = recordRequest(t, r)
		w.Write(beanOK(map[string]any{"success": true, "id": "rule-1", "name": "boss-wechat", "requestId": "req-9"}))
	}))
	defer server.Close()

	c := &Client{APIKey: "Bearer ak-123", BaseURL: server.URL}
	out, err := c.Create("老板发微信时红灯快闪")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" || got.APIKey != "ak-123" {
		t.Errorf("expected POST with stripped Bearer key, got %+v", got)
	}
	if got.Body["ruleText"] != "老板发微信时红灯快闪" {
		t.Errorf("ruleText not sent: %+v", got.Body)
	}
	if out["id"] != "rule-1" || out["name"] != "boss-wechat" || out["requestId"] != "req-9" {
		t.Errorf("unexpected create result: %+v", out)
	}
}

func TestCreateRejectsEmptyRuleTextAndMissingID(t *testing.T) {
	c := &Client{APIKey: "ak", BaseURL: "http://unused.invalid"}
	if _, err := c.Create("  "); err == nil || err.(*APIError).Code != "INVALID_PARAMS" {
		t.Errorf("expected INVALID_PARAMS, got %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(beanOK(map[string]any{"success": true}))
	}))
	defer server.Close()
	c.BaseURL = server.URL
	if _, err := c.Create("有通知时亮灯"); err == nil || err.(*APIError).Code != "INVALID_RESPONSE" {
		t.Errorf("expected INVALID_RESPONSE on missing id/name, got %v", err)
	}
}

func TestListNormalizesRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(beanOK(map[string]any{"success": true, "rules": []any{
			map[string]any{"name": "minimal"},
			map[string]any{"id": "r2", "name": "full", "title": "满配", "enabled": false, "repeat_times": float64(3), "segments": []any{map[string]any{"mode": "steady"}}},
		}}))
	}))
	defer server.Close()

	rules, err := (&Client{APIKey: "ak", BaseURL: server.URL}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	m := rules[0]
	if m["id"] != "minimal" || m["title"] != "minimal" || m["enabled"] != true || m["repeat_times"] != float64(1) || m["type"] != "light-rule" {
		t.Errorf("defaults not applied: %+v", m)
	}
	if segs, ok := m["segments"].([]any); !ok || len(segs) != 0 {
		t.Errorf("segments default should be empty array: %+v", m["segments"])
	}
	f := rules[1]
	if f["id"] != "r2" || f["title"] != "满配" || f["enabled"] != false || f["repeat_times"] != float64(3) {
		t.Errorf("explicit fields overwritten: %+v", f)
	}
}

func TestUpdatePatchesSlugIdentifierDirectly(t *testing.T) {
	var reqs []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, recordRequest(t, r))
		w.Write(beanOK(map[string]any{"success": true, "id": "boss-wechat", "name": "boss-wechat", "title": "老板微信"}))
	}))
	defer server.Close()

	out, err := (&Client{APIKey: "ak", BaseURL: server.URL}).Update("boss-wechat.json", map[string]any{
		"enabled": false, "matchRules": "should-be-dropped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].Method != "PATCH" || reqs[0].Path != "/boss-wechat" {
		t.Fatalf("expected single direct PATCH, got %+v", reqs)
	}
	if _, ok := reqs[0].Body["matchRules"]; ok {
		t.Errorf("non-whitelisted patch key should be dropped: %+v", reqs[0].Body)
	}
	if reqs[0].Body["enabled"] != false {
		t.Errorf("enabled not sent: %+v", reqs[0].Body)
	}
	if out["updated"] != true || out["title"] != "老板微信" {
		t.Errorf("unexpected update result: %+v", out)
	}
}

func TestUpdateFallsBackToListResolutionOn404(t *testing.T) {
	var reqs []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordRequest(t, r)
		reqs = append(reqs, rec)
		switch {
		case rec.Method == "PATCH" && rec.Path == "/boss-wechat":
			w.WriteHeader(404)
			w.Write([]byte(`{"msg":"not found"}`))
		case rec.Method == "GET":
			w.Write(beanOK(map[string]any{"success": true, "rules": []any{
				map[string]any{"id": "uuid-1", "name": "boss-wechat"},
			}}))
		case rec.Method == "PATCH" && rec.Path == "/uuid-1":
			w.Write(beanOK(map[string]any{"success": true, "id": "uuid-1", "name": "boss-wechat"}))
		default:
			t.Errorf("unexpected request: %+v", rec)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()

	out, err := (&Client{APIKey: "ak", BaseURL: server.URL}).Update("boss-wechat", map[string]any{"ruleText": "改成绿灯"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 3 {
		t.Fatalf("expected direct PATCH + list + resolved PATCH, got %+v", reqs)
	}
	if out["id"] != "uuid-1" || out["updated"] != true {
		t.Errorf("unexpected result: %+v", out)
	}
}

func TestUpdateRequiresPatchFieldsAndIdentifier(t *testing.T) {
	c := &Client{APIKey: "ak", BaseURL: "http://unused.invalid"}
	if _, err := c.Update("x", map[string]any{}); err == nil || err.(*APIError).Code != "INVALID_PARAMS" {
		t.Errorf("empty patch should fail INVALID_PARAMS, got %v", err)
	}
	if _, err := c.Update("  ", map[string]any{"enabled": true}); err == nil || err.(*APIError).Code != "INVALID_PARAMS" {
		t.Errorf("empty identifier should fail INVALID_PARAMS, got %v", err)
	}
}

func TestDeleteResolvesIdentifierThenDeletes(t *testing.T) {
	var reqs []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordRequest(t, r)
		reqs = append(reqs, rec)
		if rec.Method == "GET" {
			w.Write(beanOK(map[string]any{"success": true, "rules": []any{
				map[string]any{"id": "uuid-7", "name": "boss-wechat"},
			}}))
			return
		}
		w.Write(beanOK(map[string]any{"success": true, "requestId": "req-1"}))
	}))
	defer server.Close()

	out, err := (&Client{APIKey: "ak", BaseURL: server.URL}).Delete("boss-wechat")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[1].Method != "DELETE" || reqs[1].Path != "/uuid-7" {
		t.Fatalf("expected list then DELETE /uuid-7, got %+v", reqs)
	}
	if out["id"] != "uuid-7" || out["name"] != "boss-wechat" || out["requestId"] != "req-1" {
		t.Errorf("unexpected delete result: %+v", out)
	}
}

func TestDeleteUnknownRuleFailsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(beanOK(map[string]any{"success": true, "rules": []any{}}))
	}))
	defer server.Close()
	_, err := (&Client{APIKey: "ak", BaseURL: server.URL}).Delete("ghost")
	if err == nil || err.(*APIError).Code != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %v", err)
	}
}

func TestRequestErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{"bean business code", 200, `{"code":"100403","msg":"配额不足"}`, "100403"},
		{"data success false", 200, `{"code":"000000","data":{"success":false,"message":"编译失败"}}`, "BUSINESS_FAILED"},
		{"http 500 plain text", 500, `boom`, "500"},
		{"invalid json", 200, `not-json`, "INVALID_RESPONSE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := (&Client{APIKey: "ak", BaseURL: server.URL}).List()
			if err == nil || err.(*APIError).Code != tc.wantCode {
				t.Errorf("expected code %s, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestRequestRequiresAPIKey(t *testing.T) {
	_, err := (&Client{BaseURL: "http://unused.invalid"}).List()
	if err == nil || err.(*APIError).Code != "AUTH_REQUIRED" {
		t.Errorf("expected AUTH_REQUIRED, got %v", err)
	}
}

func TestJwtMissingErrorGetsDecorated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"msg":"JWT is missing"}`))
	}))
	defer server.Close()
	_, err := (&Client{APIKey: "ak", BaseURL: server.URL + APIPath}).List()
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 401 {
		t.Fatalf("expected 401 APIError, got %v", err)
	}
	if !strings.Contains(apiErr.Message, "X-Api-Key-Id") {
		t.Errorf("jwt error should be decorated: %s", apiErr.Message)
	}
}
