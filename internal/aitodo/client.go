// Package aitodo implements the cloud AI TODO contract shared by CLI consumers.
package aitodo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/envhost"
)

const APIPath = "/api/message/todo/agent"

type Fields = map[string]any
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string     { return e.Message }
func invalid(message string) error { return &Error{"INVALID_PARAMS", message} }
func Payload(err error) Fields {
	code := "INTERNAL_ERROR"
	message := "AI TODO operation failed"
	if e, ok := err.(*Error); ok {
		code = e.Code
		message = e.Message
	}
	return Fields{"ok": false, "error": Fields{"code": code, "message": message}}
}
func NewRequestKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func RequestID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "agent_todo_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

type Client struct {
	APIKey, Host, BaseURL, Language string
	HTTPClient                      *http.Client
}

func (c *Client) request(ctx context.Context, path string, body Fields) (Fields, error) {
	base := c.BaseURL
	if base == "" {
		host := envhost.Normalize(c.Host)
		if host == "" {
			host = envhost.Host()
		}
		base = "https://" + host + APIPath
	}
	key := strings.TrimSpace(c.APIKey)
	if len(key) >= 7 && strings.EqualFold(key[:7], "Bearer ") {
		key = strings.TrimSpace(key[7:])
	}
	if key == "" {
		return nil, &Error{"AUTH_REQUIRED", "Configure the plugin API Key before using AI TODO"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, invalid("Request is not valid JSON")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, invalid("Invalid API URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", key)
	lang := c.Language
	if lang == "" {
		lang = "zh-CN"
	}
	req.Header.Set("Accept-Language", lang)
	client := http.Client{}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, &Error{"REQUEST_FAILED", "AI TODO request failed or timed out; write outcome may be unknown"}
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if err != nil {
		return nil, &Error{"REQUEST_FAILED", "AI TODO response interrupted; write outcome may be unknown"}
	}
	if len(raw) > 4*1024*1024 {
		return nil, badResponse()
	}
	var bean Fields
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	parseErr := dec.Decode(&bean)
	if parseErr == nil {
		var trailing any
		if dec.Decode(&trailing) != io.EOF {
			return nil, badResponse()
		}
	}
	code, _ := bean["code"].(string)
	if res.StatusCode < 200 || res.StatusCode >= 300 || (code != "" && code != "000000") {
		if code == "" || code == "000000" {
			code = fmt.Sprintf("HTTP_%d", res.StatusCode)
		}
		msg, _ := bean["msg"].(string)
		if msg == "" && parseErr != nil {
			msg = strings.TrimSpace(string(raw))
		}
		if msg == "" {
			msg = "AI TODO request failed"
		}
		msg = strings.ReplaceAll(msg, key, "[REDACTED]")
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		return nil, &Error{code, msg}
	}
	data, ok := bean["data"].(map[string]any)
	if parseErr != nil || code != "000000" || !ok || data == nil {
		return nil, badResponse()
	}
	return data, nil
}
func badResponse() error {
	return &Error{"INVALID_RESPONSE", "AI TODO returned an invalid response; write outcome may be unknown"}
}
func validID(v any) bool { s, ok := v.(string); return ok && strings.TrimSpace(s) != "" }
func checkItem(v any) (Fields, error) {
	item, ok := v.(map[string]any)
	if !ok {
		return nil, badResponse()
	}
	_, titleOK := item["title"].(string)
	_, fullOK := item["isFullDay"].(bool)
	_, doneOK := item["isDone"].(bool)
	_, timePresent := item["dueAt"]
	kind := item["todoType"]
	if !validID(item["todoId"]) || !titleOK || !fullOK || !doneOK || !timePresent || !validMillis(item["dueAt"]) || (kind != "normal" && kind != "onboarding_guide" && kind != "onboarding_rule") {
		return nil, badResponse()
	}
	return item, nil
}
func (c *Client) Execute(ctx context.Context, action string, raw Fields, requestKey string) (Fields, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	p, err := Validate(action, raw)
	if err != nil {
		return nil, err
	}
	request := func(path string, body Fields) (Fields, error) { return c.request(ctx, path, body) }
	switch action {
	case "create":
		if requestKey == "" {
			requestKey, err = NewRequestKey()
			if err != nil {
				return nil, err
			}
		}
		p["requestId"] = RequestID(requestKey)
		data, e := request("create", p)
		if ae, ok := e.(*Error); ok && ae.Code == "REQUEST_FAILED" && ctx.Err() == nil {
			data, e = request("create", p)
		}
		if e != nil {
			return nil, e
		}
		_, ok := data["duplicated"].(bool)
		if !validID(data["todoId"]) || !ok {
			return nil, badResponse()
		}
		data["ok"] = true
		return data, nil
	case "list":
		data, e := request("query", p)
		if e != nil {
			return nil, e
		}
		items, ok := data["items"].([]any)
		_, lastOK := data["isLastPage"].(bool)
		if !ok || !lastOK || !sameNumber(data["pageNo"], p["pageNo"]) {
			return nil, badResponse()
		}
		for _, item := range items {
			if _, e = checkItem(item); e != nil {
				return nil, e
			}
		}
		data["ok"] = true
		return data, nil
	case "get":
		data, e := request("detail", p)
		if e != nil {
			return nil, e
		}
		item, e := checkItem(data["item"])
		if e != nil {
			return nil, e
		}
		if item["todoId"] != p["todoId"] {
			return nil, badResponse()
		}
		data["ok"] = true
		return data, nil
	case "delete":
		return c.process(ctx, "deleteBatch", Fields{"todoIds": p["todoIds"], "deleteReason": "manual_delete"}, p["todoIds"].([]string))
	}
	patch := Fields{}
	for k, v := range p {
		if k != "isDone" {
			patch[k] = v
		}
	}
	hasContent := len(patch) > 1
	var updated Fields
	if hasContent {
		data, e := request("detail", Fields{"todoId": p["todoId"]})
		if e != nil {
			return nil, e
		}
		current, e := checkItem(data["item"])
		if e != nil {
			return nil, e
		}
		if current["todoId"] != p["todoId"] {
			return nil, badResponse()
		}
		if current["todoType"] != "normal" {
			return nil, &Error{"NOT_EDITABLE", "Onboarding todos cannot be edited"}
		}
		merged := Fields{}
		for _, k := range []string{"title", "dueAt", "isFullDay"} {
			merged[k] = current[k]
			if v, exists := patch[k]; exists {
				merged[k] = v
			}
		}
		if v, exists := patch["dueAt"]; exists && v == nil {
			merged["isFullDay"] = false
			patch["isFullDay"] = false
		}
		if _, e = Validate("create", merged); e != nil {
			return nil, e
		}
		data, e = request("update", patch)
		if e != nil {
			return nil, e
		}
		updated, e = checkItem(data["item"])
		if e != nil {
			return nil, e
		}
		if updated["todoId"] != p["todoId"] {
			return nil, badResponse()
		}
	}
	if done, exists := p["isDone"]; exists {
		ids := []string{p["todoId"].(string)}
		result, e := c.process(ctx, "status", Fields{"todoIds": ids, "isDone": done}, ids)
		if e != nil {
			if !hasContent {
				return nil, e
			}
			result = Payload(e)
			result["statusOutcome"] = "unknown"
		}
		result["todoId"] = p["todoId"]
		result["contentUpdated"] = hasContent
		result["statusUpdated"] = result["ok"] == true
		if result["ok"] == true {
			result["isDone"] = done
		}
		return result, nil
	}
	return Fields{"ok": true, "contentUpdated": true, "item": updated}, nil
}
func (c *Client) process(ctx context.Context, path string, body Fields, ids []string) (Fields, error) {
	data, err := c.request(ctx, path, body)
	if err != nil {
		return nil, err
	}
	raw, ok := data["processedTodoIds"].([]any)
	if !ok {
		return nil, badResponse()
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	processed := map[string]bool{}
	for _, v := range raw {
		id, ok := v.(string)
		if !ok || !allowed[id] {
			return nil, badResponse()
		}
		processed[id] = true
	}
	missing := []string{}
	for _, id := range ids {
		if !processed[id] {
			missing = append(missing, id)
		}
	}
	return Fields{"ok": len(missing) == 0, "processedTodoIds": raw, "unprocessedTodoIds": missing}, nil
}
