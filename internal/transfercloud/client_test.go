package transfercloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testID = "0123456789abcdef0123456789abcdef"
const testKey = "users/data-transfer/20260928/" + testID + ".tar.gz"
const signedURL = "https://bucket.oss-cn-hangzhou.aliyuncs.com/" + testKey + "?signature=secret-signature"

func reply(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": data})
}
func testTask() map[string]any {
	return map[string]any{"taskId": testID, "objectKey": testKey, "bucket": "bucket", "region": "cn-hangzhou", "endpoint": "https://oss-cn-hangzhou.aliyuncs.com", "credentials": map[string]string{"accessKeyId": "temporary-id", "accessKeySecret": "temporary-secret", "securityToken": "temporary-token", "expiration": time.Now().Add(time.Hour).Format(time.RFC3339)}}
}
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("cli-key", "unused")
	c.baseURL = srv.URL + apiPath
	return c
}
func TestTaskAPIUsesCLIKeyAndHidesURL(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Api-Key-Id") != "cli-key" {
			t.Error("CLI API key missing")
		}
		for name := range r.Header {
			if strings.Contains(strings.ToLower(name), "scope") {
				t.Error("unexpected scope header")
			}
		}
		var body map[string]string
		if r.Method != "DELETE" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		switch r.URL.Path {
		case apiPath + "/create":
			if body["fileType"] != "DATA_TRANSFER" || body["fileName"] != "migration.tar.gz" {
				t.Error("wrong create body")
			}
			reply(w, testTask())
		case apiPath + "/complete":
			if body["taskId"] != testID || body["objectKey"] != testKey {
				t.Error("wrong confirmation body")
			}
			reply(w, map[string]any{"taskId": testID, "objectKey": testKey, "status": "COMPLETED", "fileSize": "263611", "signedUrl": signedURL, "expiresAt": "1790600000000"})
		case apiPath + "/download-url":
			if body["taskId"] != testID {
				t.Error("wrong download body")
			}
			reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL})
		case apiPath + "/delete":
			if r.Method != "DELETE" || r.URL.Query().Get("taskId") != testID {
				t.Error("wrong delete request")
			}
			reply(w, true)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	if _, err := c.Create(ctx, "migration.tar.gz"); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.Complete(ctx, testID, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.FileSize != 263611 {
		t.Fatal("string file size was not decoded")
	}
	raw, _ := json.Marshal(receipt)
	if strings.Contains(string(raw), "signature") {
		t.Fatal("signed URL leaked")
	}
	if _, err = c.downloadURL(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if err = c.Delete(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("calls %d", calls)
	}
}
func TestFailuresAreRedactedAndCreateNotRetried(t *testing.T) {
	for _, status := range []int{403, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"code":"999999","msg":"cli-key temporary-secret secret-signature","data":null}`)
			})
			_, err := c.Create(context.Background(), "migration.tar.gz")
			if err == nil {
				t.Fatal("expected error")
			}
			for _, secret := range []string{"cli-key", "temporary-secret", "secret-signature"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("secret leaked")
				}
			}
			if calls != 1 {
				t.Fatalf("create retried %d times", calls)
			}
		})
	}
}
func TestRejectInvalidCredentialsAndLinks(t *testing.T) {
	for _, raw := range []string{"http://bucket.oss-cn-hangzhou.aliyuncs.com/a", "https://oss-cn-hangzhou.aliyuncs.com.evil.test/a", "https://user@oss-cn-hangzhou.aliyuncs.com/a", "https://127.0.0.1/a", "https://oss-cn-hangzhou.aliyuncs.com:444/a"} {
		if _, err := validateOSSURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	task := testTask()
	task["objectKey"] = "users/data-transfer/20260928/other.tar.gz"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, task) })
	if _, err := c.Create(context.Background(), "migration.tar.gz"); err == nil {
		t.Fatal("accepted mismatched object key")
	}
	for _, raw := range []string{`-1`, `"1.5"`, `null`, `"9223372036854775808"`} {
		var n Number
		if json.Unmarshal([]byte(raw), &n) == nil {
			t.Errorf("accepted size %s", raw)
		}
	}
}
func TestAPIRefusesRedirectAndMissingKey(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := c.Create(context.Background(), "migration.tar.gz"); err == nil || reached {
		t.Fatal("redirect followed")
	}
	c.apiKey = ""
	if _, err := c.Create(context.Background(), "migration.tar.gz"); err == nil {
		t.Fatal("accepted missing key")
	}
}
