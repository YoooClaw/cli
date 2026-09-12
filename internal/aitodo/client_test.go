package aitodo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func item() Fields {
	return Fields{"todoId": "9007199254740993", "todoType": "normal", "title": "会议", "dueAt": nil, "isFullDay": false, "isDone": false}
}
func serve(t *testing.T, fn func(string, Fields) (int, any)) (*Client, func()) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Api-Key-Id") != "test" || r.Header.Get("Accept-Language") == "" {
			t.Error("invalid method/auth/language")
		}
		var b Fields
		d := json.NewDecoder(r.Body)
		d.UseNumber()
		if e := d.Decode(&b); e != nil {
			t.Error(e)
		}
		status, data := fn(r.URL.Path, b)
		w.WriteHeader(status)
		if raw, ok := data.(string); ok {
			fmt.Fprint(w, raw)
		} else {
			json.NewEncoder(w).Encode(Fields{"code": "000000", "data": data})
		}
	}))
	return &Client{APIKey: "Bearer test", BaseURL: s.URL + APIPath}, s.Close
}
func TestAllEndpointsAndPartialStatus(t *testing.T) {
	var paths []string
	c, close := serve(t, func(path string, b Fields) (int, any) {
		paths = append(paths, path)
		switch path {
		case APIPath + "/create":
			if len(b["requestId"].(string)) > 64 {
				t.Error("long requestId")
			}
			return 200, Fields{"todoId": item()["todoId"], "duplicated": false}
		case APIPath + "/query":
			if b["isDone"] != nil {
				t.Error("all statuses lost")
			}
			return 200, Fields{"items": []any{item()}, "pageNo": b["pageNo"], "isLastPage": true}
		case APIPath + "/detail":
			return 200, Fields{"item": item()}
		case APIPath + "/update":
			v := item()
			v["title"] = b["title"]
			return 200, Fields{"item": v}
		case APIPath + "/status":
			return 401, "Jwt is missing"
		case APIPath + "/deleteBatch":
			if b["deleteReason"] != "manual_delete" {
				t.Error("wrong deletion reason")
			}
			return 200, Fields{"processedTodoIds": []string{item()["todoId"].(string)}}
		}
		t.Fatal(path)
		return 500, nil
	})
	defer close()
	ctx := context.Background()
	id := item()["todoId"]
	for _, tc := range []struct {
		a string
		p Fields
	}{{"create", Fields{"title": "会议", "dueAt": nil, "isFullDay": false}}, {"list", Fields{"isDone": nil}}, {"get", Fields{"todoId": id}}} {
		if _, e := c.Execute(ctx, tc.a, tc.p, "call"); e != nil {
			t.Fatal(e)
		}
	}
	out, e := c.Execute(ctx, "update", Fields{"todoId": id, "title": "审批会议", "isDone": true}, "")
	if e != nil {
		t.Fatal(e)
	}
	if out["ok"] != false || out["contentUpdated"] != true || out["statusUpdated"] != false {
		t.Fatalf("partial state lost: %#v", out)
	}
	if _, e = c.Execute(ctx, "delete", Fields{"todoIds": []string{id.(string)}, "confirmed": true}, ""); e != nil {
		t.Fatal(e)
	}
	want := []string{"create", "query", "detail", "detail", "update", "status", "deleteBatch"}
	for i, p := range want {
		if paths[i] != APIPath+"/"+p {
			t.Fatal(paths)
		}
	}
}
func TestCreateRetryUsesSameRequestID(t *testing.T) {
	var ids []any
	c, close := serve(t, func(_ string, b Fields) (int, any) {
		ids = append(ids, b["requestId"])
		return 200, Fields{"todoId": "1", "duplicated": false}
	})
	defer close()
	p := Fields{"title": "x", "dueAt": nil, "isFullDay": false}
	for _, key := range []string{strings.Repeat("a", 1000), strings.Repeat("a", 1000), "other"} {
		if _, e := c.Execute(context.Background(), "create", p, key); e != nil {
			t.Fatal(e)
		}
	}
	if ids[0] != ids[1] || ids[0] == ids[2] || len(ids[0].(string)) != 54 {
		t.Fatal(ids)
	}
	// Drop first response after receiving the request to model an unknown write outcome.
	var attempts int
	var first string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b Fields
		json.NewDecoder(r.Body).Decode(&b)
		id := b["requestId"].(string)
		attempts++
		if attempts == 1 {
			first = id
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if id != first {
			t.Error("retry changed ID")
		}
		json.NewEncoder(w).Encode(Fields{"code": "000000", "data": Fields{"todoId": "1", "duplicated": true}})
	}))
	defer server.Close()
	c.BaseURL = server.URL
	out, e := c.Execute(context.Background(), "create", p, "retry")
	if e != nil || out["duplicated"] != true || attempts != 2 {
		t.Fatalf("%v %v %d", out, e, attempts)
	}
}
func TestBusinessAndHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		status    int
		raw, code string
	}{{401, "Jwt is missing", "HTTP_401"}, {200, `{"code":"910001","msg":"dueAt 不能为空","data":null}`, "910001"}, {200, `{"code":"000000","data":null}`, "INVALID_RESPONSE"}, {200, `{"code":"000000","data":{"todoId":1,"duplicated":false}}`, "INVALID_RESPONSE"}} {
		t.Run(tc.code+fmt.Sprint(tc.status), func(t *testing.T) {
			n := 0
			c, close := serve(t, func(_ string, _ Fields) (int, any) { n++; return tc.status, tc.raw })
			defer close()
			_, e := c.Execute(context.Background(), "create", Fields{"title": "x", "dueAt": nil, "isFullDay": false}, "k")
			if e == nil || e.(*Error).Code != tc.code || n != 1 {
				t.Fatalf("error %v attempts %d", e, n)
			}
		})
	}
}
func TestStatusOnlyAndMissingIDs(t *testing.T) {
	var paths []string
	c, close := serve(t, func(p string, b Fields) (int, any) {
		paths = append(paths, p)
		return 200, Fields{"processedTodoIds": []string{}}
	})
	defer close()
	for _, done := range []bool{true, false} {
		v, e := c.Execute(context.Background(), "update", Fields{"todoId": "1", "isDone": done}, "")
		if e != nil || v["ok"] != false || v["contentUpdated"] != false {
			t.Fatal(v, e)
		}
	}
	if !reflect.DeepEqual(paths, []string{APIPath + "/status", APIPath + "/status"}) {
		t.Fatal(paths)
	}
}
func TestRejectRedirectAndForeignID(t *testing.T) {
	leaked := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 307) }))
	defer src.Close()
	c := Client{APIKey: "test", BaseURL: src.URL}
	_, err := c.Execute(context.Background(), "get", Fields{"todoId": "1"}, "")
	if err == nil || leaked {
		t.Fatal("redirect followed", err)
	}
	c2, close := serve(t, func(string, Fields) (int, any) { return 200, Fields{"processedTodoIds": []string{"foreign"}} })
	defer close()
	if _, e := c2.Execute(context.Background(), "delete", Fields{"todoIds": []string{"1"}, "confirmed": true}, ""); e == nil {
		t.Fatal("accepted foreign processed ID")
	}
}
func TestContentFailureStopsStatus(t *testing.T) {
	calls := 0
	c, close := serve(t, func(p string, b Fields) (int, any) { calls++; return 401, "Jwt is missing" })
	defer close()
	if _, err := c.Execute(context.Background(), "update", Fields{"todoId": "1", "title": "x", "isDone": true}, ""); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestTitleOnlyCreateDefaultsBeforeHTTP(t *testing.T) {
	c, close := serve(t, func(path string, body Fields) (int, any) {
		if path != APIPath+"/create" || !rawHas(body, "dueAt") || body["dueAt"] != nil || body["isFullDay"] != false {
			t.Fatalf("unexpected request: %s %#v", path, body)
		}
		return 200, Fields{"todoId": "1309", "duplicated": false}
	})
	defer close()
	input := Fields{"title": "洗衣服"}
	if _, err := c.Execute(context.Background(), "create", input, "test-key"); err != nil {
		t.Fatal(err)
	}
	if len(input) != 1 {
		t.Fatal("input mutated")
	}
}
