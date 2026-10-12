package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/fsutil"
)

type Attempt struct {
	Official  bool      `json:"official,omitempty"`
	Identity  Identity  `json:"identity"`
	UpdateID  string    `json:"updateId"`
	Trigger   string    `json:"trigger"`
	Artifact  Artifact  `json:"artifact"`
	Target    string    `json:"target"`
	OldHash   string    `json:"oldHash"`
	CreatedAt time.Time `json:"createdAt"`
	Ready     bool      `json:"ready"`
}
type Event struct {
	ReportID         string            `json:"reportId"`
	UpdateID         string            `json:"updateId,omitempty"`
	DeviceID         string            `json:"deviceId"`
	Trigger          string            `json:"trigger"`
	Stage            string            `json:"stage"`
	Result           string            `json:"result"`
	From             string            `json:"from,omitempty"`
	To               string            `json:"to,omitempty"`
	RunningVersion   string            `json:"runningVersion"`
	InstalledVersion string            `json:"installedVersion,omitempty"`
	ErrorCode        string            `json:"errorCode,omitempty"`
	ErrorMessage     string            `json:"errorMessage,omitempty"`
	Env              map[string]string `json:"env"`
	TS               time.Time         `json:"ts"`
}

var secretPattern = regexp.MustCompile(`(?i)(?:ock[-_][A-Za-z0-9_-]+|Bearer\s+[^\s"']+)`)

func safe(message string) string {
	for _, key := range creds.ResolveAPIKeyEntries().Entries {
		if key.Key != "" {
			message = strings.ReplaceAll(message, key.Key, "[REDACTED]")
		}
	}
	message = secretPattern.ReplaceAllString(message, "[REDACTED]")
	if len([]rune(message)) > 1024 {
		message = string([]rune(message)[:1024])
	}
	return message
}

// CommandFailure retains CLI diagnostics after local redaction, before they can
// reach worker stderr or the persistent reporting queue.
func CommandFailure(action string, output []byte) error {
	return fail("CLI_COMMAND_FAILED", safe(action+": "+strings.TrimSpace(string(output))))
}

func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "UPDATE_FAILED"
}
func EventFor(a Attempt, stage, result string, err error) Event {
	id := a.Identity
	e := Event{ReportID: ID(), UpdateID: a.UpdateID, DeviceID: canonicalDeviceID(id.DeviceID), Trigger: a.Trigger, Stage: stage, Result: result, From: id.CurrentVersion, To: a.Artifact.TargetVersion, RunningVersion: id.CurrentVersion, Env: map[string]string{"platform": id.Platform, "arch": id.Arch, "clawKind": "cli", "installProtocol": "native"}, TS: time.Now().UTC()}
	if err != nil {
		e.ErrorCode = ErrorCode(err)
		e.ErrorMessage = safe(err.Error())
	}
	return e
}
func Record(a Attempt, stage, result string, err error) {
	_ = recordEvent(a.Identity, EventFor(a, stage, result, err))
}
func recordEvent(id Identity, e Event) error {
	dir := id.Directory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	release, err := lock(filepath.Join(dir, "events.lock"))
	if err != nil {
		return err
	}
	defer release()
	e.ErrorMessage = safe(e.ErrorMessage)
	if err := fsutil.WriteJSON(filepath.Join(dir, e.ReportID+".event.json"), e, 0600); err != nil {
		return err
	}
	events := readEvents(dir)
	for _, old := range events[:max(0, len(events)-200)] {
		_ = os.Remove(filepath.Join(dir, old.ReportID+".event.json"))
	}
	return nil
}
func readEvents(dir string) []Event {
	files, _ := os.ReadDir(dir)
	events := []Event{}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".event.json") {
			continue
		}
		var e Event
		path := filepath.Join(dir, f.Name())
		ok, err := fsutil.ReadJSON(path, &e)
		if !ok || err != nil || time.Since(e.TS) > 7*24*time.Hour {
			_ = os.Remove(path)
			continue
		}
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].TS.Before(events[j].TS) })
	return events
}

type retryState struct {
	Next      time.Time `json:"next"`
	Attempts  int       `json:"attempts"`
	Blocked   bool      `json:"blocked"`
	Delivered bool      `json:"delivered"`
}

func Flush(ctx context.Context, c *Client, id Identity) {
	dir := id.Directory()
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	release, err := lock(filepath.Join(dir, "sender.lock"))
	if err != nil {
		return
	}
	defer release()
	flushEvents(ctx, c, id)
	flushResults(ctx, c, id)
}
func flushEvents(ctx context.Context, c *Client, id Identity) {
	key, err := c.Key()
	if err != nil {
		return
	}
	dir := id.Directory()
	retryPath := filepath.Join(dir, "retry.json")
	var retry retryState
	_, _ = fsutil.ReadJSON(retryPath, &retry)
	if retry.Blocked || time.Now().Before(retry.Next) {
		return
	}
	events := readEvents(dir)
	batch := []Event{}
	var raw []byte
	for _, e := range events[:min(50, len(events))] {
		e.DeviceID = canonicalDeviceID(e.DeviceID)
		next := append(append([]Event{}, batch...), e)
		b, _ := json.Marshal(map[string]any{"apiKey": key, "events": next})
		if len(b) > 64<<10 {
			break
		}
		batch = next
		raw = b
	}
	if len(batch) == 0 {
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", id.BaseURL+ReportPath, bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	discard := false
	delay := time.Minute
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			var ack struct {
				Accepted *int `json:"accepted"`
				Rejected *int `json:"rejected"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ack) == nil && ack.Accepted != nil && ack.Rejected != nil && *ack.Accepted >= 0 && *ack.Rejected >= 0 && *ack.Accepted+*ack.Rejected == len(batch) {
				discard = true
			}
		}
		if resp.StatusCode == 400 || resp.StatusCode == 413 || resp.StatusCode == 422 {
			discard = true
		}
		retry.Blocked = resp.StatusCode == 401 || resp.StatusCode == 403
		if resp.StatusCode == 429 {
			if d, e := time.ParseDuration(resp.Header.Get("Retry-After") + "s"); e == nil && d > delay {
				delay = d
			}
		}
	}
	if discard {
		for _, e := range batch {
			_ = os.Remove(filepath.Join(dir, e.ReportID+".event.json"))
		}
		_ = os.Remove(retryPath)
		return
	}
	retry.Attempts++
	retry.Next = time.Now().Add(delay)
	_ = fsutil.WriteJSON(retryPath, retry, 0600)
}

type terminal struct {
	Body      map[string]any `json:"body"`
	Retry     retryState     `json:"retry"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Final uses exclusive creation: retrying a stage can never replace a terminal result.
func Final(a Attempt, err error) {
	if a.Official || a.UpdateID == "" || a.Artifact.TargetVersion == "" {
		return
	}
	dir := a.Identity.Directory()
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	body := map[string]any{"updateId": a.UpdateID, "fromVersion": a.Identity.CurrentVersion, "targetVersion": a.Artifact.TargetVersion}
	if err != nil {
		body["errorCode"] = ErrorCode(err)
		body["errorMessage"] = safe(err.Error())
	}
	raw, _ := json.Marshal(terminal{Body: body, CreatedAt: time.Now().UTC()})
	path := filepath.Join(dir, a.UpdateID+".result.json")
	file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	_, _ = file.Write(raw)
	_ = file.Close()
}
func flushResults(ctx context.Context, c *Client, id Identity) {
	files, _ := os.ReadDir(id.Directory())
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".result.json") {
			continue
		}
		path := filepath.Join(id.Directory(), f.Name())
		var t terminal
		if ok, err := fsutil.ReadJSON(path, &t); !ok || err != nil {
			continue
		}
		if time.Since(t.CreatedAt) > 7*24*time.Hour {
			_ = os.Remove(path)
			continue
		}
		if t.Retry.Blocked || t.Retry.Delivered || t.Retry.Attempts >= 4 || time.Now().Before(t.Retry.Next) {
			continue
		}
		t.Retry.Attempts++
		t.Retry.Next = time.Now().Add([]time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 30 * time.Minute}[t.Retry.Attempts-1])
		if fsutil.WriteJSON(path, t, 0600) != nil {
			return
		}
		var accepted bool
		err := c.request(ctx, id, ResultPath, t.Body, &accepted)
		t.Retry.Delivered = err == nil && accepted
		code := ErrorCode(err)
		t.Retry.Blocked = code == "HTTP_401" || code == "HTTP_403" || code == "000019" || code == "OTA00004" || code == "AUTH_CHANGED" || code == "ENV_CHANGED"
		_ = fsutil.WriteJSON(path, t, 0600)
		return
	}
}
func Heartbeat(id Identity) {
	path := filepath.Join(id.Directory(), "heartbeat.json")
	if os.MkdirAll(id.Directory(), 0700) != nil {
		return
	}
	release, err := lock(filepath.Join(id.Directory(), "heartbeat.lock"))
	if err != nil {
		return
	}
	defer release()
	var last time.Time
	_, _ = fsutil.ReadJSON(path, &last)
	if time.Since(last) < 24*time.Hour {
		return
	}
	e := EventFor(Attempt{Identity: id, Trigger: "system"}, "heartbeat", "succeeded", nil)
	e.From = ""
	if recordEvent(id, e) == nil {
		_ = fsutil.WriteJSON(path, time.Now().UTC(), 0600)
	}
}
