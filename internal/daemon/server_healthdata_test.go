package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

const healthBatch = `{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T09:10:00+08:00","days":[{"date":"2026-10-19","sleepSessions":[],"items":[{"metric":"steps","name":"Steps","value":9007199254740993,"unit":"steps"}]}]}`

func TestHealthDataReplacesDailySnapshotAndAcceptsIngestCredentials(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "phone-key"}}}
	for _, token := range []string{"phone-key", "gateway-secret"} {
		request := httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(healthBatch))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		assertHealthReceipt(t, response.Code, response.Body.Bytes(), 1, 0)
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})) != 2 || !bytes.Contains(raw, []byte(`"value":9007199254740993`)) {
		t.Fatalf("snapshot not replaced faithfully: %s", raw)
	}
	health := httptest.NewRecorder()
	srv.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !strings.Contains(health.Body.String(), `"health-data"`) || !strings.Contains(health.Body.String(), `"health-data-daily-v3"`) || !isIngestPath("/health-data") {
		t.Fatalf("health-data capability/auth path missing: %s", health.Body.String())
	}
}

func TestHealthDataRejectsOldFormatsWithoutWriting(t *testing.T) {
	srv, _ := newTestServer(t, "")
	for _, body := range []string{`{"schemaVersion":1,"items":[]}`, `{"schemaVersion":2,"days":[]}`, `{"items":[]}`} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(srv.ctx.Paths.HealthData); !os.IsNotExist(err) {
		t.Fatal("old format wrote data")
	}
}

func TestHealthDataInvalidBatchDoesNotAppendOrModifyPreviousData(t *testing.T) {
	srv, _ := newTestServer(t, "")
	if err := os.MkdirAll(filepath.Dir(healthDataFile(srv)), 0o700); err != nil {
		t.Fatal(err)
	}
	prior := []byte("{\"items\":[]}\n")
	if err := os.WriteFile(healthDataFile(srv), prior, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		``, `null`, `[]`, `{}`, `{"items":null}`, `{"items":[{},null]}`,
		`{"items":[{}],"deletedIds":["valid",null]}`, `{"items":[],"deletedIds":[1]}`,
		`{"items":[]}{"items":[]}`, `{"items":`,
	} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s: response=%d %s", body, response.Code, response.Body.String())
		}
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil || !bytes.Equal(raw, prior) {
		t.Fatalf("invalid input modified old data: %s err=%v", raw, err)
	}
}

func TestHealthDataAuthAndWriteFailureNeverAcknowledgeSuccess(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	for _, token := range []string{"", "unknown-key"} {
		request := httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(healthBatch))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized response=%d %s", response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(srv.ctx.Paths.HealthData); !os.IsNotExist(err) {
		t.Fatalf("unauthorized input wrote health data: %v", err)
	}
	if err := os.MkdirAll(healthDataFile(srv), 0o700); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(healthBatch))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"HEALTH_DATA_WRITE_FAILED"`) {
		t.Fatalf("write failure acknowledged: %d %s", response.Code, response.Body.String())
	}
}

func TestHealthDataRelayRoundTripIncludesReceiptsAndDailyJSONL(t *testing.T) {
	srv, local := newTestServer(t, "gateway-secret")
	daily := `{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-09-30T16:00:00+08:00","days":[{"date":"2026-09-30","sleepSessions":[],"items":[{"metric":"steps","name":"Steps","value":31502,"unit":"steps"}]}]}`
	payloads := []string{healthBatch, healthBatch, daily, daily}
	type result struct {
		responses []relay.ProxyResponseFrame
		err       error
	}
	done := make(chan result, 1)
	upgrader := websocket.Upgrader{}
	fakeRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var responses []relay.ProxyResponseFrame
		for i, body := range payloads {
			if err := conn.WriteJSON(relay.Frame{
				"type": "request", "id": fmt.Sprintf("health-test-%d", i), "method": "POST", "path": "health-data", "body": body,
			}); err != nil {
				done <- result{err: err}
				return
			}
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					done <- result{err: err}
					return
				}
				if string(raw) == "ping" {
					_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
					continue
				}
				var response relay.ProxyResponseFrame
				if err := json.Unmarshal(raw, &response); err != nil {
					done <- result{err: err}
					return
				}
				responses = append(responses, response)
				break
			}
		}
		done <- result{responses: responses}
	}))
	defer fakeRelay.Close()
	client := relay.NewClient(relay.ClientOptions{
		TunnelURL:          "ws" + strings.TrimPrefix(fakeRelay.URL, "http"),
		CredentialProvider: func() (relay.Credential, error) { return relay.Credential{}, nil }, Logger: srv.logger,
	})
	dispatcher := relay.NewDispatcher(relay.DispatcherOptions{
		Client: client, HTTPBaseURL: local.URL, HTTPToken: "gateway-secret", ClientLabel: "phone", Logger: srv.logger,
	})
	dispatcher.Start()
	client.Start()
	defer client.Stop("test complete")
	select {
	case got := <-done:
		if got.err != nil || len(got.responses) != len(payloads) {
			t.Fatalf("Relay responses=%+v err=%v", got.responses, got.err)
		}
		for i, response := range got.responses {
			if response.Type != "proxy_response" || response.ID != fmt.Sprintf("health-test-%d", i) {
				t.Fatalf("unexpected Relay response: %+v", response)
			}
			assertHealthReceipt(t, response.Status, []byte(response.Body), 1, 0)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health-data Relay response timed out")
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil || len(bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})) != 2 ||
		!bytes.Contains(raw, []byte(`"value":9007199254740993`)) {
		t.Fatalf("Relay batch did not reach JSONL unchanged: %s err=%v", raw, err)
	}
	dailyRaw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.HealthData, "2026-09-30.jsonl"))
	if err != nil || len(bytes.Split(bytes.TrimSpace(dailyRaw), []byte{'\n'})) != 2 || bytes.Count(dailyRaw, []byte(`"value":31502`)) != 1 {
		t.Fatalf("Relay daily replays were not replaced once by business date: %s err=%v", dailyRaw, err)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "POST health-data -> /health-data") ||
		!strings.Contains(string(logs), "requestId=health-test-0 ingested=1 deleted=0") ||
		!strings.Contains(string(logs), "requestId=health-test-1 ingested=1 deleted=0") ||
		!strings.Contains(string(logs), "requestId=health-test-3 ingested=1 deleted=0") {
		t.Fatalf("missing actual request/storage result logs: %s err=%v", logs, err)
	}
}

func TestHealthDataPartialFailureWritesValidDaysAndLogsReason(t *testing.T) {
	srv, _ := newTestServer(t, "")
	body := `{"schemaVersion":3,"provider":"honor","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T09:10:00+08:00","days":[` +
		`{"date":"2026-10-19","sleepSessions":[],"items":[{"metric":"steps","name":"Steps","value":1,"unit":"steps"}]},` +
		`{"date":"2026-10-20","sleepSessions":[],"items":[{"metric":"steps","name":"","value":1,"unit":"steps"}]}]}`
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("partial validation failure rejected the request: %d %s", response.Code, response.Body.String())
	}
	var receipt struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Rejected int  `json:"rejected"`
		Days     []struct {
			Date    string `json:"date"`
			Applied bool   `json:"applied"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"days"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.Ingested != 1 || receipt.Rejected != 1 || len(receipt.Days) != 2 {
		t.Fatalf("receipt=%+v body=%s", receipt, response.Body.String())
	}
	if !receipt.Days[0].Applied || receipt.Days[0].Reason != "" || receipt.Days[0].Message != "" ||
		receipt.Days[1].Applied || receipt.Days[1].Reason != "invalid_snapshot" || !strings.Contains(receipt.Days[1].Message, "items[0]") {
		t.Fatalf("day decisions=%+v", receipt.Days)
	}
	if _, err := os.Stat(filepath.Join(srv.ctx.Paths.HealthData, "2026-10-19.jsonl")); err != nil {
		t.Fatalf("valid day not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.ctx.Paths.HealthData, "2026-10-20.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("invalid day written: %v", err)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "date=2026-10-20 applied=false reason=invalid_snapshot") ||
		!strings.Contains(string(logs), "rejected=1") || !strings.Contains(string(logs), "items[0]") {
		t.Fatalf("missing validation failure log: %s err=%v", logs, err)
	}
}

func TestHealthDataRecordDropKeepsDayAndReportsWarnings(t *testing.T) {
	srv, _ := newTestServer(t, "")
	body := `{"schemaVersion":3,"provider":"honor","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T09:10:00+08:00","days":[` +
		`{"date":"2026-10-19","sleepSessions":[],` +
		`"items":[{"metric":"steps","name":"Steps","value":1,"unit":"steps"}],` +
		`"workouts":[{"id":"bad-workout","activity":"running","name":"Run","startAt":"2026-10-19T08:00:00+08:00","endAt":"2026-10-19T08:30:00+08:00","durationMinutes":-1,"items":[]}]}]}`
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("record drop rejected the request: %d %s", response.Code, response.Body.String())
	}
	var receipt struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Rejected int  `json:"rejected"`
		Days     []struct {
			Applied  bool     `json:"applied"`
			Dropped  int      `json:"dropped"`
			Warnings []string `json:"warnings"`
		} `json:"days"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.Ingested != 1 || receipt.Rejected != 0 || len(receipt.Days) != 1 ||
		!receipt.Days[0].Applied || receipt.Days[0].Dropped != 1 ||
		!strings.Contains(strings.Join(receipt.Days[0].Warnings, "\n"), "workouts[0]") {
		t.Fatalf("receipt=%+v body=%s", receipt, response.Body.String())
	}
	stored, err := os.ReadFile(healthDataFile(srv))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("bad-workout")) || !bytes.Contains(stored, []byte(`"value":1`)) {
		t.Fatalf("day not written partially: %s", stored)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "date=2026-10-19 applied=true dropped=1") ||
		!strings.Contains(string(logs), "dropped=1") {
		t.Fatalf("missing record drop log: %s err=%v", logs, err)
	}
}

func TestHealthDataAllInvalidDaysStillReturnsPerDayReasons(t *testing.T) {
	srv, _ := newTestServer(t, "")
	body := `{"schemaVersion":3,"provider":"honor","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T09:10:00+08:00","days":[` +
		`{"date":"2026-10-19","sleepSessions":[],"items":[{"metric":"Steps","name":"Steps","value":1,"unit":"steps"}]}]}`
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("all-invalid batch should stay inspectable: %d %s", response.Code, response.Body.String())
	}
	var receipt struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Rejected int  `json:"rejected"`
		Days     []struct {
			Applied bool   `json:"applied"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"days"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.Ingested != 0 || receipt.Rejected != 1 || len(receipt.Days) != 1 ||
		receipt.Days[0].Applied || receipt.Days[0].Reason != "invalid_snapshot" || receipt.Days[0].Message == "" {
		t.Fatalf("receipt=%+v body=%s", receipt, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(srv.ctx.Paths.HealthData, "2026-10-19.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("invalid-only batch wrote a day file: %v", err)
	}
}

func healthDataFile(srv *server) string {
	return filepath.Join(srv.ctx.Paths.HealthData, "2026-10-19.jsonl")
}

func assertHealthReceipt(t *testing.T, status int, raw []byte, items, deleted int) {
	t.Helper()
	var receipt struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Deleted  int  `json:"deleted"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || status != http.StatusOK ||
		!receipt.OK || receipt.Ingested != items || receipt.Deleted != deleted {
		t.Fatalf("receipt=%d %s err=%v, want ok=true ingested=%d deleted=%d", status, raw, err, items, deleted)
	}
}

func TestHealthDataRejectsFarFutureCollectedAt(t *testing.T) {
	srv, _ := newTestServer(t, "")
	body := fmt.Sprintf(`{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":%q,"days":[{"date":%q,"sleepSessions":[],"items":[{"metric":"steps","name":"Steps","value":1,"unit":"steps"}]}]}`,
		time.Now().Add(48*time.Hour).Format(time.RFC3339), time.Now().Format("2006-01-02"))
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("future collectedAt accepted: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(srv.ctx.Paths.HealthData); !os.IsNotExist(err) {
		t.Fatalf("rejected batch wrote health data: %v", err)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "rejected: collectedAt is too far in the future") {
		t.Fatalf("missing request rejection log: %s err=%v", logs, err)
	}
}

func TestHealthDetailsHTTPReceiptAndStoredRecords(t *testing.T) {
	raw, err := os.ReadFile("../healthdata/testdata/health-details-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	srv, local := newTestServer(t, "health-details-secret")
	request, _ := http.NewRequest(http.MethodPost, local.URL+"/health-data", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer health-details-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var receipt struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
	}
	if err = json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !receipt.OK || receipt.Ingested != 6 {
		t.Fatal(response.StatusCode, receipt)
	}
	stored, err := os.ReadFile(filepath.Join(srv.ctx.Paths.HealthData, "2026-11-05.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"synthetic-medication", "synthetic-workout", "synthetic-intake", "synthetic-mood"} {
		if !bytes.Contains(stored, []byte(key)) {
			t.Fatal("missing record", key)
		}
	}
}

func TestHealthDataReceiptReportsAppliedAndSkippedDays(t *testing.T) {
	srv, _ := newTestServer(t, "")
	send := func(provider, collected string, dates ...string) []struct {
		Date    string `json:"date"`
		Applied bool   `json:"applied"`
		Reason  string `json:"reason"`
	} {
		t.Helper()
		days := make([]map[string]any, 0, len(dates))
		for _, date := range dates {
			days = append(days, map[string]any{"date": date, "sleepSessions": []any{}, "items": []any{map[string]any{"metric": "steps", "name": "Steps", "value": 1, "unit": "steps"}}})
		}
		body, err := json.Marshal(map[string]any{"schemaVersion": 3, "provider": provider, "timeZone": "Asia/Shanghai", "collectedAt": collected, "days": days})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", bytes.NewReader(body)))
		assertHealthReceipt(t, response.Code, response.Body.Bytes(), len(dates), 0)
		var receipt struct {
			Days []struct {
				Date    string `json:"date"`
				Applied bool   `json:"applied"`
				Reason  string `json:"reason"`
			} `json:"days"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if len(receipt.Days) != len(dates) {
			t.Fatalf("missing decisions: %s", response.Body.String())
		}
		return receipt.Days
	}
	first := send(" HealthKit ", "2026-10-19T09:10:00+08:00", "2026-10-18")
	if !first[0].Applied || first[0].Reason != "" {
		t.Fatalf("first: %+v", first)
	}
	mixed := send("honor", "2026-10-19T09:20:00+08:00", "2026-10-18", "2026-10-19")
	if mixed[0].Date != "2026-10-18" || mixed[0].Applied || mixed[0].Reason != "apple_priority" || mixed[1].Date != "2026-10-19" || !mixed[1].Applied || mixed[1].Reason != "" {
		t.Fatalf("mixed: %+v", mixed)
	}
	stale := send("HEALTHKIT", "2026-10-19T09:00:00+08:00", "2026-10-18")
	if stale[0].Applied || stale[0].Reason != "stale_collected_at" {
		t.Fatalf("stale: %+v", stale)
	}
	// Apple can supersede the newer Android snapshot, then replay identically.
	for i := 0; i < 2; i++ {
		applied := send("healthkit", "2026-10-19T09:00:00+08:00", "2026-10-19")
		if !applied[0].Applied || applied[0].Reason != "" {
			t.Fatalf("applied: %+v", applied)
		}
	}
}
