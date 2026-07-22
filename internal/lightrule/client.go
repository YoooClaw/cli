package lightrule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/envhost"
)

// 本文件是云端灯效规则 client（Go 版 NotificationIntelligenceLightRuleClient，
// 对齐 openclaw plugin 的 src/light-rules/client.ts）：CRUD 全部打到
// Notification Intelligence Service 的插件侧 API，X-Api-Key-Id 鉴权。
// create 只接收自然语言 ruleText，由云端独立 Agent 编译出 name/title/segments。
// （本地文件存储见 storage.go，仅供 daemon/gateway 兼容路径使用。）

// APIPath 是插件侧灯效规则 API 前缀（与 plugin env.ts 的常量一致）。
const APIPath = "/api/plugin/notification-intelligence/light-rules"

// APIURL 返回云端灯效规则 API 端点。host 由调用方从 config.ResolveCloudHost 解析后
// 传入；传空则回落到 PHONE_NOTIFICATIONS_ENV 的环境默认值（见 internal/envhost）。
func APIURL(host string) string {
	resolved := envhost.Normalize(host)
	if resolved == "" {
		resolved = envhost.Host()
	}
	return "https://" + resolved + APIPath
}

// APIError 是云端请求的结构化错误。Code 可能是业务码（INVALID_PARAMS /
// NOT_FOUND / BUSINESS_FAILED / AUTH_REQUIRED / INVALID_RESPONSE）或 HTTP 状态码字符串。
type APIError struct {
	Code    string
	Message string
	Status  int
}

func (e *APIError) Error() string { return e.Message }

// ClientLogger 是 client 依赖的最小日志接口。
type ClientLogger interface {
	Info(string)
	Warn(string)
}

// Client 是云端灯效规则 client。零值不可用，APIKey 必填。
type Client struct {
	APIKey     string
	Host       string       // 空则回落到环境默认主机；BaseURL 非空时忽略
	BaseURL    string       // 空则用 APIURL(Host)
	Logger     ClientLogger // 可为 nil
	HTTPClient *http.Client // 可为 nil，默认 30s 超时
}

// updatePatchKeys 是 PATCH 允许透传的字段白名单（对齐 TS sanitizeUpdatePatch）。
var updatePatchKeys = []string{"ruleText", "title", "description", "enabled", "segments", "repeat_times", "repeat"}

// simpleIdentifierRE 判定可直接当 URL 路径段 PATCH 的标识符（对齐 TS shouldPatchIdentifierDirectly）。
var simpleIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// List 拉取当前账号的全部灯效规则（含 enabled 状态），字段缺省按 plugin 同款规则补齐。
func (c *Client) List() ([]map[string]any, error) {
	data, err := c.request("GET", "", nil)
	if err != nil {
		return nil, err
	}
	raw, _ := data["rules"].([]any)
	rules := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		rule, nerr := normalizeRuleRecord(item)
		if nerr != nil {
			return nil, nerr
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Create 提交自然语言 ruleText，云端 Agent 编译并保存规则。
func (c *Client) Create(ruleText string) (map[string]any, error) {
	ruleText = strings.TrimSpace(ruleText)
	if ruleText == "" {
		return nil, &APIError{Code: "INVALID_PARAMS", Message: "ruleText is required"}
	}
	data, err := c.request("POST", "", map[string]any{"ruleText": ruleText})
	if err != nil {
		return nil, err
	}
	id, name := nonEmptyString(data["id"]), nonEmptyString(data["name"])
	if id == "" || name == "" {
		return nil, &APIError{Code: "INVALID_RESPONSE", Message: "Notification Intelligence create response missing id/name"}
	}
	out := map[string]any{"id": id, "name": name}
	copyOptional(out, data, "requestId", "warning")
	return out, nil
}

// Update 按 id/name PATCH 规则。patch 里传 ruleText 表示云端重编译，
// 传 title/description/enabled/segments/repeat_times/repeat 表示普通字段局部更新。
// 简单标识符先直接 PATCH，404 再走 list 解析（对齐 TS update 流程）。
func (c *Client) Update(identifier string, patch map[string]any) (map[string]any, error) {
	body := map[string]any{}
	for _, key := range updatePatchKeys {
		if v, ok := patch[key]; ok && v != nil {
			body[key] = v
		}
	}
	if len(body) == 0 {
		return nil, &APIError{Code: "INVALID_PARAMS", Message: "at least one update field is required"}
	}

	normalized := normalizeLookupName(identifier)
	if normalized == "" {
		return nil, &APIError{Code: "INVALID_PARAMS", Message: "rule id/name is required"}
	}

	if simpleIdentifierRE.MatchString(normalized) {
		c.logInfo("Light rules API update: direct PATCH identifier=" + normalized)
		out, err := c.patchRule(normalized, normalized, body)
		if err == nil || !isNotFound(err) {
			return out, err
		}
		c.logInfo("Light rules API update: direct PATCH missed identifier=" + normalized + "; resolving by list")
	}

	id, name, err := c.resolveIdentifier(normalized)
	if err != nil {
		return nil, err
	}
	c.logInfo("Light rules API update: PATCH targetId=" + id + " identifier=" + normalized)
	return c.patchRule(id, name, body)
}

// Delete 按 id/name 删除规则（云端当前实现为软删除）。
func (c *Client) Delete(identifier string) (map[string]any, error) {
	id, name, err := c.resolveIdentifier(identifier)
	if err != nil {
		return nil, err
	}
	data, err := c.request("DELETE", "/"+urlPathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id":   firstNonEmpty(nonEmptyString(data["id"]), id),
		"name": firstNonEmpty(nonEmptyString(data["name"]), name),
	}
	copyOptional(out, data, "requestId")
	return out, nil
}

func (c *Client) patchRule(id, name string, body map[string]any) (map[string]any, error) {
	data, err := c.request("PATCH", "/"+urlPathEscape(id), body)
	if err != nil {
		return nil, err
	}
	var rule map[string]any
	if data["rule"] != nil {
		if rule, err = normalizeRuleRecord(data["rule"]); err != nil {
			return nil, err
		}
	}
	out := map[string]any{
		"id":      firstNonEmpty(nonEmptyString(data["id"]), nonEmptyString(rule["id"]), id),
		"name":    firstNonEmpty(nonEmptyString(data["name"]), nonEmptyString(rule["name"]), name),
		"updated": true,
	}
	if title := firstNonEmpty(nonEmptyString(data["title"]), nonEmptyString(rule["title"])); title != "" {
		out["title"] = title
	}
	if rule != nil {
		out["rule"] = rule
	}
	copyOptional(out, data, "requestId", "warning")
	return out, nil
}

func (c *Client) resolveIdentifier(identifier string) (id, name string, err error) {
	normalized := normalizeLookupName(identifier)
	if normalized == "" {
		return "", "", &APIError{Code: "INVALID_PARAMS", Message: "rule id/name is required"}
	}
	rules, err := c.List()
	if err != nil {
		return "", "", err
	}
	for _, rule := range rules {
		if rule["id"] == normalized || rule["name"] == normalized {
			return nonEmptyString(rule["id"]), nonEmptyString(rule["name"]), nil
		}
	}
	return "", "", &APIError{Code: "NOT_FOUND", Message: "灯效规则 '" + normalized + "' 不存在"}
}

// request 发起一次云端请求并解包 ResponseBean{code,msg,data}：
// code!="000000" 或 data.success==false 均视为失败（对齐 TS request）。
func (c *Client) request(method, path string, body map[string]any) (map[string]any, error) {
	apiKey := strings.TrimSpace(c.APIKey)
	if apiKey == "" {
		return nil, &APIError{Code: "AUTH_REQUIRED", Message: "API Key 未配置，请先 `yoooclaw auth set-api-key -`"}
	}

	base := c.BaseURL
	if base == "" {
		base = APIURL(c.Host)
	}
	url := base + path

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, &APIError{Code: "INVALID_PARAMS", Message: err.Error()}
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, &APIError{Code: "INVALID_PARAMS", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", strings.TrimPrefix(apiKey, "Bearer "))

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	startedAt := time.Now()
	res, err := client.Do(req)
	if err != nil {
		c.logWarn(fmt.Sprintf("Light rules API: %s %s failed: %v", method, url, err))
		return nil, &APIError{Code: "NETWORK_ERROR", Message: err.Error()}
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(res.Body)

	var parsed map[string]any
	parseErr := json.Unmarshal(text, &parsed)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message := remoteErrorMessage(parsed, string(text), res.StatusCode)
		c.logWarn(fmt.Sprintf("Light rules API: %s %s failed status=%d message=%s", method, url, res.StatusCode, message))
		return nil, &APIError{Code: strconv.Itoa(res.StatusCode), Message: decorateRemoteErrorMessage(message, url, apiKey), Status: res.StatusCode}
	}
	if parseErr != nil || parsed == nil {
		return nil, &APIError{Code: "INVALID_RESPONSE", Message: "empty or invalid JSON response"}
	}

	if code := nonEmptyString(parsed["code"]); code != "" && code != "000000" {
		msg := firstNonEmpty(nonEmptyString(parsed["msg"]), nonEmptyString(parsed["message"]), "remote request failed")
		return nil, &APIError{Code: code, Message: msg}
	}

	data, ok := parsed["data"].(map[string]any)
	if !ok {
		if parsed["data"] == nil {
			data = parsed
		} else {
			return nil, &APIError{Code: "INVALID_RESPONSE", Message: "response data is empty"}
		}
	}
	if success, isBool := data["success"].(bool); isBool && !success {
		msg := firstNonEmpty(nonEmptyString(data["message"]), "remote business request failed")
		return nil, &APIError{Code: "BUSINESS_FAILED", Message: msg}
	}

	c.logInfo(fmt.Sprintf("Light rules API: %s %s ok elapsedMs=%d", method, url, time.Since(startedAt).Milliseconds()))
	return data, nil
}

func (c *Client) logInfo(msg string) {
	if c.Logger != nil {
		c.Logger.Info(msg)
	}
}

func (c *Client) logWarn(msg string) {
	if c.Logger != nil {
		c.Logger.Warn(msg)
	}
}

// normalizeRuleRecord 补齐云端规则记录的缺省字段（对齐 TS normalizeRuleRecord）。
func normalizeRuleRecord(value any) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, &APIError{Code: "INVALID_RESPONSE", Message: "invalid light rule item"}
	}
	name := nonEmptyString(raw["name"])
	id := firstNonEmpty(nonEmptyString(raw["id"]), name)
	if id == "" || name == "" {
		return nil, &APIError{Code: "INVALID_RESPONSE", Message: "light rule item missing id/name"}
	}
	rule := map[string]any{}
	for k, v := range raw {
		rule[k] = v
	}
	rule["id"] = id
	rule["name"] = name
	rule["title"] = firstNonEmpty(nonEmptyString(raw["title"]), name)
	rule["type"] = "light-rule"
	rule["description"] = firstNonEmpty(nonEmptyString(raw["description"]), name)
	if _, ok := raw["segments"].([]any); !ok {
		rule["segments"] = []any{}
	}
	if _, ok := raw["repeat_times"].(float64); !ok {
		rule["repeat_times"] = float64(1)
	}
	if _, ok := raw["enabled"].(bool); !ok {
		rule["enabled"] = true
	}
	if nonEmptyString(raw["createdAt"]) == "" {
		rule["createdAt"] = time.Unix(0, 0).UTC().Format(time.RFC3339)
	}
	return rule, nil
}

func remoteErrorMessage(parsed map[string]any, text string, status int) string {
	if parsed != nil {
		if msg := firstNonEmpty(nonEmptyString(parsed["msg"]), nonEmptyString(parsed["message"])); msg != "" {
			return msg
		}
		if data, ok := parsed["data"].(map[string]any); ok {
			if msg := nonEmptyString(data["message"]); msg != "" {
				return msg
			}
		}
	}
	if msg := strings.TrimSpace(text); msg != "" {
		return msg
	}
	return "HTTP " + strconv.Itoa(status)
}

// decorateRemoteErrorMessage 为两类 401 补充排障指引：
// “jwt is missing” 说明网关没把插件前缀放行到 API Key 鉴权（对齐 TS 同名函数）；
// “Invalid plugin API Key” 说明 key 本身不被插件侧接受——多半是环境不对或拿了非
// plugin key（如 CLI 签发的 ock-cli-*），此时 Relay 隧道照样连得上，极难自查。
func decorateRemoteErrorMessage(message, url, apiKey string) string {
	if strings.Contains(strings.ToLower(message), "invalid plugin api key") {
		return message + creds.PluginAPIKeyRejectedHint(apiKey)
	}
	if !strings.Contains(strings.ToLower(message), "jwt is missing") {
		return message
	}
	if strings.Contains(url, APIPath) {
		return message + "。请求已命中插件侧 API " + APIPath + "，插件侧鉴权应只使用 " +
			"X-Api-Key-Id；当前 401 来自云端 JWT 鉴权层，说明该插件前缀没有被网关/服务端放行到 API Key 鉴权。" +
			"请后端检查网关白名单和 Notification Intelligence Service 插件路由：" + url
	}
	return message + "。灯效规则应调用插件侧 API " + APIPath + "，并通过 X-Api-Key-Id 鉴权；" +
		"当前请求可能命中了 App 端 JWT 接口：" + url
}

func isNotFound(err error) bool {
	e, ok := err.(*APIError)
	if !ok {
		return false
	}
	return e.Status == 404 || e.Code == "404" || e.Code == "NOT_FOUND"
}

func nonEmptyString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func copyOptional(dst, src map[string]any, keys ...string) {
	for _, key := range keys {
		if v := nonEmptyString(src[key]); v != "" {
			dst[key] = v
		}
	}
}

func urlPathEscape(s string) string {
	// 与 TS encodeURIComponent 对齐：规则 id 可能含非 ASCII。
	escaped := strings.Builder{}
	for _, b := range []byte(s) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			escaped.WriteByte(b)
		} else {
			escaped.WriteString(fmt.Sprintf("%%%02X", b))
		}
	}
	return escaped.String()
}
