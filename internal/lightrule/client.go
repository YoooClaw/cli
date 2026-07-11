package lightrule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/envhost"
	"github.com/YoooClaw/cli/internal/errs"
)

// CloudClient 对齐 OpenClaw plugin 的 Notification Intelligence 灯效规则客户端。
type CloudClient struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type RemoteError struct {
	Code    string
	Message string
	Status  int
}

type resolvedIdentifier struct {
	ID   string
	Name string
}

func (e *RemoteError) Error() string { return e.Message }

var directIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// NewCloudClient 使用当前账号默认 API key 和当前环境端点。
func NewCloudClient() (*CloudClient, error) {
	apiKey := strings.TrimSpace(creds.ResolveAPIKey().Value)
	if apiKey == "" {
		return nil, errs.New(errs.CodeCredentialMissing, "API Key 未设置，请先执行 yc auth set-api-key <apiKey>")
	}
	return &CloudClient{
		APIKey:     stripBearer(apiKey),
		BaseURL:    envhost.NotificationIntelligenceLightRulesURL(),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *CloudClient) List() ([]any, error) {
	data, err := c.request(http.MethodGet, "", nil)
	if err != nil {
		return nil, err
	}
	rules, _ := data["rules"].([]any)
	if rules == nil {
		rules = []any{}
	}
	for index, item := range rules {
		rule, err := normalizeRuleRecord(item)
		if err != nil {
			return nil, err
		}
		rules[index] = rule
	}
	return rules, nil
}

func (c *CloudClient) Create(ruleText string) (map[string]any, error) {
	trimmed := strings.TrimSpace(ruleText)
	if trimmed == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "ruleText is required")
	}
	result, err := c.request(http.MethodPost, "", map[string]any{"ruleText": trimmed})
	if err != nil {
		return nil, err
	}
	if stringField(result, "id") == "" || stringField(result, "name") == "" {
		return nil, &RemoteError{Code: "INVALID_RESPONSE", Message: "Notification Intelligence create response missing id/name"}
	}
	return result, nil
}

func (c *CloudClient) Update(identifier string, patch map[string]any) (map[string]any, error) {
	patch = sanitizeUpdatePatch(patch)
	normalized := normalizeIdentifier(identifier)
	if normalized == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "rule id/name is required")
	}
	if len(patch) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "at least one update field is required")
	}
	if directIdentifierRE.MatchString(normalized) {
		result, err := c.patchResolvedRule(resolvedIdentifier{ID: normalized, Name: normalized}, patch)
		if err == nil {
			return result, nil
		}
		remote, ok := err.(*RemoteError)
		if !ok || (remote.Status != http.StatusNotFound && remote.Code != "NOT_FOUND") {
			return nil, err
		}
	}
	resolved, err := c.resolveIdentifier(normalized)
	if err != nil {
		return nil, err
	}
	return c.patchResolvedRule(resolved, patch)
}

func (c *CloudClient) Delete(identifier string) (map[string]any, error) {
	resolved, err := c.resolveIdentifier(identifier)
	if err != nil {
		return nil, err
	}
	result, err := c.request(http.MethodDelete, "/"+url.PathEscape(resolved.ID), nil)
	if err != nil {
		return nil, err
	}
	if stringField(result, "id") == "" {
		result["id"] = resolved.ID
	}
	if stringField(result, "name") == "" {
		result["name"] = resolved.Name
	}
	return result, nil
}

func (c *CloudClient) patchResolvedRule(resolved resolvedIdentifier, patch map[string]any) (map[string]any, error) {
	result, err := c.request(http.MethodPatch, "/"+url.PathEscape(resolved.ID), patch)
	if err != nil {
		return nil, err
	}
	if nested, ok := result["rule"]; ok && nested != nil {
		rule, normalizeErr := normalizeRuleRecord(nested)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		result["rule"] = rule
		if stringField(result, "id") == "" {
			result["id"] = stringField(rule, "id")
		}
		if stringField(result, "name") == "" {
			result["name"] = stringField(rule, "name")
		}
		if stringField(result, "title") == "" {
			result["title"] = stringField(rule, "title")
		}
	}
	if stringField(result, "id") == "" {
		result["id"] = resolved.ID
	}
	if stringField(result, "name") == "" {
		result["name"] = resolved.Name
	}
	result["updated"] = true
	return result, nil
}

func (c *CloudClient) resolveIdentifier(identifier string) (resolvedIdentifier, error) {
	normalized := normalizeIdentifier(identifier)
	if normalized == "" {
		return resolvedIdentifier{}, errs.New(errs.CodeInvalidArgument, "rule id/name is required")
	}
	rules, err := c.List()
	if err != nil {
		return resolvedIdentifier{}, err
	}
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		if rule == nil {
			continue
		}
		id, name := stringField(rule, "id"), stringField(rule, "name")
		if id == normalized || name == normalized {
			return resolvedIdentifier{ID: id, Name: name}, nil
		}
	}
	return resolvedIdentifier{}, errs.New(errs.CodeNotFound, "灯效规则 '"+normalized+"' 不存在")
}

func (c *CloudClient) request(method, path string, body map[string]any) (map[string]any, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = envhost.NotificationIntelligenceLightRulesURL()
	} else {
		base = envhost.NormalizeNotificationIntelligenceLightRulesURL(base)
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "无法编码灯效规则请求："+err.Error())
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "无法构造灯效规则请求："+err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", stripBearer(c.APIKey))
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "灯效规则请求失败："+err.Error())
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "读取灯效规则响应失败："+err.Error())
	}
	parsed := map[string]any{}
	if len(bytes.TrimSpace(responseBody)) > 0 && json.Unmarshal(responseBody, &parsed) != nil {
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return nil, &RemoteError{Code: "INVALID_RESPONSE", Message: "invalid JSON response", Status: res.StatusCode}
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := parsed["data"].(map[string]any)
		message := firstNonEmpty(stringField(parsed, "msg"), stringField(parsed, "message"), stringField(data, "message"), strings.TrimSpace(string(responseBody)), fmt.Sprintf("HTTP %d", res.StatusCode))
		return nil, &RemoteError{Code: fmt.Sprintf("%d", res.StatusCode), Message: message, Status: res.StatusCode}
	}
	if len(bytes.TrimSpace(responseBody)) == 0 || parsed == nil {
		return nil, &RemoteError{Code: "INVALID_RESPONSE", Message: "empty or invalid JSON response", Status: res.StatusCode}
	}
	if code := stringField(parsed, "code"); code != "" && code != "000000" {
		return nil, &RemoteError{Code: code, Message: firstNonEmpty(stringField(parsed, "msg"), stringField(parsed, "message"), "remote request failed"), Status: res.StatusCode}
	}
	data := parsed
	if nested, ok := parsed["data"].(map[string]any); ok && nested != nil {
		data = nested
	}
	if data["success"] == false {
		return nil, &RemoteError{Code: "BUSINESS_FAILED", Message: firstNonEmpty(stringField(data, "message"), "remote business request failed"), Status: res.StatusCode}
	}
	return data, nil
}

func normalizeRuleRecord(value any) (map[string]any, error) {
	rule, ok := value.(map[string]any)
	if !ok || rule == nil {
		return nil, &RemoteError{Code: "INVALID_RESPONSE", Message: "invalid light rule item"}
	}
	name := stringField(rule, "name")
	id := firstNonEmpty(stringField(rule, "id"), name)
	if id == "" || name == "" {
		return nil, &RemoteError{Code: "INVALID_RESPONSE", Message: "light rule item missing id/name"}
	}
	rule["id"] = id
	rule["name"] = name
	if stringField(rule, "title") == "" {
		rule["title"] = name
	}
	rule["type"] = "light-rule"
	if stringField(rule, "description") == "" {
		rule["description"] = name
	}
	if _, ok := rule["segments"].([]any); !ok {
		rule["segments"] = []any{}
	}
	if _, ok := rule["repeat_times"].(float64); !ok {
		rule["repeat_times"] = float64(1)
	}
	if _, ok := rule["enabled"].(bool); !ok {
		rule["enabled"] = true
	}
	if stringField(rule, "createdAt") == "" {
		rule["createdAt"] = "1970-01-01T00:00:00.000Z"
	}
	return rule, nil
}

func sanitizeUpdatePatch(params map[string]any) map[string]any {
	patch := map[string]any{}
	for _, key := range []string{"ruleText", "title", "description", "enabled", "segments", "repeat_times", "repeat"} {
		if value, ok := params[key]; ok && value != nil {
			patch[key] = value
		}
	}
	return patch
}

func normalizeIdentifier(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasSuffix(strings.ToLower(trimmed), ".json") {
		return trimmed[:len(trimmed)-len(".json")]
	}
	return trimmed
}

func stripBearer(value string) string {
	trimmed := strings.TrimSpace(value)
	return strings.TrimPrefix(trimmed, "Bearer ")
}

func stringField(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
