package lightrule

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCloudClientCRUD(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("X-Api-Key-Id"); got != "ock_test_key" {
			t.Fatalf("api key header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true,"rules":[{"id":"rule-1","name":"boss-wechat","enabled":true}]}}`))
		case r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ruleText"] != "老板发微信时红灯快闪" {
				t.Fatalf("create body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true,"id":"rule-1","name":"boss-wechat"}}`))
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true,"id":"rule-1","name":"boss-wechat","updated":true}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true,"id":"rule-1","name":"boss-wechat"}}`))
		}
	}))
	defer server.Close()

	client := &CloudClient{APIKey: "Bearer ock_test_key", BaseURL: server.URL, HTTPClient: server.Client()}
	created, err := client.Create("老板发微信时红灯快闪")
	if err != nil || created["id"] != "rule-1" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	updated, err := client.Update("rule-1", map[string]any{"enabled": false})
	if err != nil || updated["updated"] != true {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	deleted, err := client.Delete("boss-wechat")
	if err != nil || deleted["id"] != "rule-1" {
		t.Fatalf("delete = %+v, %v", deleted, err)
	}
	basePath := "/api/plugin/notification-intelligence/light-rules"
	want := []string{"POST " + basePath, "PATCH " + basePath + "/rule-1", "GET " + basePath, "DELETE " + basePath + "/rule-1"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestCloudClientBusinessFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"000000","data":{"success":false,"message":"compile failed"}}`))
	}))
	defer server.Close()

	client := &CloudClient{APIKey: "key", BaseURL: server.URL, HTTPClient: server.Client()}
	_, err := client.Create("test notification rule")
	remote, ok := err.(*RemoteError)
	if !ok || remote.Code != "BUSINESS_FAILED" || remote.Message != "compile failed" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCloudClientRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		call func(*CloudClient) error
	}{
		{
			name: "empty response",
			body: "",
			call: func(client *CloudClient) error {
				_, err := client.List()
				return err
			},
		},
		{
			name: "create missing id and name",
			body: `{"code":"000000","data":{"success":true}}`,
			call: func(client *CloudClient) error {
				_, err := client.Create("微信消息时亮红灯")
				return err
			},
		},
		{
			name: "invalid list item",
			body: `{"code":"000000","data":{"success":true,"rules":[{"enabled":true}]}}`,
			call: func(client *CloudClient) error {
				_, err := client.List()
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := &CloudClient{APIKey: "key", BaseURL: server.URL, HTTPClient: server.Client()}
			remote, ok := test.call(client).(*RemoteError)
			if !ok || remote.Code != "INVALID_RESPONSE" {
				t.Fatalf("error = %#v", remote)
			}
		})
	}
}

func TestCloudClientSanitizesUpdatePatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !reflect.DeepEqual(body, map[string]any{"enabled": false}) {
			t.Fatalf("patch body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true}}`))
	}))
	defer server.Close()
	client := &CloudClient{APIKey: "key", BaseURL: server.URL, HTTPClient: server.Client()}
	result, err := client.Update("rule-1", map[string]any{"enabled": false, "unexpected": "ignored"})
	if err != nil || result["id"] != "rule-1" || result["updated"] != true {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}
