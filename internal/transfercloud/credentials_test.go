package transfercloud

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func uploadTaskFixture(t *testing.T) UploadTask {
	t.Helper()
	raw, _ := json.Marshal(testTask())
	var task UploadTask
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	return task
}
func TestUploadCredentialsConcurrentRefresh(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != apiPath+"/refresh" {
			t.Error("unexpected endpoint")
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 1 || body["taskId"] != testID {
			t.Error("refresh payload must only contain taskId")
		}
		next := testTask()
		next["credentials"].(map[string]string)["securityToken"] = "renewed-token"
		reply(w, next)
	})
	task := uploadTaskFixture(t)
	task.Credentials.Expiration = time.Now().Add(time.Minute).Format(time.RFC3339)
	p := &uploadCredentials{cloud: c, task: task}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			creds, err := p.GetCredentials(context.Background())
			if err != nil || creds.SecurityToken != "renewed-token" {
				t.Errorf("credentials refresh failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
	p.invalidate()
	if _, err := p.GetCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("server-expired credentials were not refreshed")
	}
}
func TestUploadCredentialsStopsOnTerminalRefreshFailure(t *testing.T) {
	for _, status := range []int{404, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) })
			task := uploadTaskFixture(t)
			task.Credentials.Expiration = time.Now().Add(-time.Minute).Format(time.RFC3339)
			p := &uploadCredentials{cloud: c, task: task}
			for i := 0; i < 3; i++ {
				if _, err := p.GetCredentials(context.Background()); err == nil {
					t.Fatal("expected refresh failure")
				}
			}
			if calls != 1 {
				t.Fatal("terminal refresh failure retried")
			}
		})
	}
}
func TestRefreshRejectsChangedTaskAndExpiredCredentials(t *testing.T) {
	for _, field := range []string{"taskId", "objectKey", "bucket", "endpoint", "region", "fileType", "fileName", "credentials"} {
		t.Run(field, func(t *testing.T) {
			next := testTask()
			switch field {
			case "credentials":
				next[field].(map[string]string)["expiration"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			case "bucket":
				next[field] = "other-bucket"
			case "endpoint":
				next[field] = "https://oss-cn-beijing.aliyuncs.com"
			case "region":
				next[field] = "cn-beijing"
			default:
				next[field] = "changed"
			}
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, next) })
			task := uploadTaskFixture(t)
			if _, err := c.Refresh(context.Background(), &task); err == nil {
				t.Fatal("accepted changed task or invalid credentials")
			}
		})
	}
}
