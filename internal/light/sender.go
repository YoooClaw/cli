package light

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Logger 是 sender 依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
}

// SendResult 是一次灯效下发的结果。
type SendResult struct {
	OK          bool   `json:"ok"`
	BizUniqueID string `json:"bizUniqueId,omitempty"`
	Response    any    `json:"response,omitempty"`
	Status      int    `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	Via         string `json:"via,omitempty"`
}

// SendOptions 对齐 OpenClaw plugin 的一次性亮灯可选参数。
type SendOptions struct {
	BizUniqueID string
}

type intelligenceAttempt struct {
	Fallback       bool
	FallbackReason string
	Result         SendResult
}

var lightHTTPClient = &http.Client{Timeout: 15 * time.Second}

// SendLightEffect 优先调用 Notification Intelligence Service 的插件侧一次性
// 亮灯 Facade；新入口未部署、网关未放行或网络不可用时回退旧 message-service。
// options 使用 variadic 保持旧调用源码兼容。
func SendLightEffect(apiKey string, segments []map[string]any, repeatInput RepeatInput, reason, title string, logger Logger, options ...SendOptions) SendResult {
	resolvedTitle := resolveLightTitle(title, reason, segments)
	if len(segments) == 0 {
		return SendResult{OK: false, Error: "segments 不能为空"}
	}
	repeatTimes, err := NormalizeRepeatTimes(repeatInput)
	if err != nil {
		return SendResult{OK: false, Error: err.Error()}
	}
	if err := AssertAncsRepeatTimes(repeatTimes); err != nil {
		return SendResult{OK: false, Error: err.Error()}
	}

	bizUniqueID := ""
	if len(options) > 0 {
		bizUniqueID = strings.TrimSpace(options[0].BizUniqueID)
	}
	attempt := sendViaNotificationIntelligence(apiKey, segments, repeatTimes, reason, resolvedTitle, bizUniqueID, logger)
	if !attempt.Fallback {
		return attempt.Result
	}
	if logger != nil {
		logger.Warn("Light sender: notification-intelligence entry unavailable (" + attempt.FallbackReason + "), falling back to legacy message-service")
	}
	return sendViaLegacyMessageService(apiKey, segments, repeatInput, reason, resolvedTitle, logger)
}

func sendViaNotificationIntelligence(apiKey string, segments []map[string]any, repeatTimes int, reason, title, bizUniqueID string, logger Logger) intelligenceAttempt {
	apiURL := LightAPIURL()
	requestBody := map[string]any{
		"title":        title,
		"repeat_times": repeatTimes,
		"segments":     segments,
	}
	if strings.TrimSpace(reason) != "" {
		requestBody["reason"] = reason
	}
	if bizUniqueID != "" {
		requestBody["bizUniqueId"] = bizUniqueID
	}
	if logger != nil {
		id := bizUniqueID
		if id == "" {
			id = "(server-generated)"
		}
		logger.Info(fmt.Sprintf("Light sender: POST %s, bizUniqueId=%s, title=%s, repeat_times=%d, segments_count=%d", apiURL, id, title, repeatTimes, len(segments)))
	}

	res, resBody, err := postJSON(apiURL, apiKey, requestBody)
	if err != nil {
		return intelligenceAttempt{Fallback: true, FallbackReason: "network error: " + err.Error(), Result: SendResult{OK: false, Error: err.Error()}}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		bodyText := string(resBody)
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed ||
			(res.StatusCode == http.StatusUnauthorized && strings.Contains(strings.ToLower(bodyText), "jwt is missing")) {
			return intelligenceAttempt{Fallback: true, FallbackReason: fmt.Sprintf("HTTP %d", res.StatusCode), Result: SendResult{OK: false, Status: res.StatusCode, Error: bodyText}}
		}
		if logger != nil {
			logger.Warn(fmt.Sprintf("Light sender: FAILED %d, url=%s, resBody=%s", res.StatusCode, apiURL, truncate(bodyText, 500)))
		}
		return intelligenceAttempt{Result: SendResult{OK: false, Status: res.StatusCode, Error: bodyText, Via: "notification-intelligence"}}
	}

	parsed, ok := parseJSONObject(resBody)
	if !ok {
		if logger != nil {
			logger.Warn("Light sender: invalid JSON from " + apiURL + ", resBody=" + truncate(string(resBody), 200))
		}
		return intelligenceAttempt{Result: SendResult{OK: false, Error: "invalid JSON response", Via: "notification-intelligence"}}
	}
	if code := stringValue(parsed["code"]); code != "" && code != "000000" {
		message := firstNonEmpty(stringValue(parsed["msg"]), stringValue(parsed["message"]), "remote code "+code)
		return intelligenceAttempt{Result: SendResult{OK: false, Error: message, Response: parsed, Via: "notification-intelligence"}}
	}
	data, _ := parsed["data"].(map[string]any)
	if data != nil && data["success"] == false {
		message := firstNonEmpty(stringValue(data["message"]), "remote business request failed")
		return intelligenceAttempt{Result: SendResult{OK: false, Error: message, Response: parsed, Via: "notification-intelligence"}}
	}
	effectiveID := bizUniqueID
	if dataID := strings.TrimSpace(stringValue(data["bizUniqueId"])); dataID != "" {
		effectiveID = dataID
	}
	if logger != nil {
		id := effectiveID
		if id == "" {
			id = "-"
		}
		logger.Info("Light sender: OK bizUniqueId=" + id + ", resBody=" + truncate(string(resBody), 200))
	}
	return intelligenceAttempt{Result: SendResult{OK: true, BizUniqueID: effectiveID, Response: parsed, Via: "notification-intelligence"}}
}

func sendViaLegacyMessageService(apiKey string, segments []map[string]any, repeatInput RepeatInput, reason, title string, logger Logger) SendResult {
	apiURL := LegacyLightAPIURL()
	templateID := strings.TrimSpace(os.Getenv("LIGHT_TEMPLATE_ID"))
	if templateID == "" {
		return SendResult{OK: false, Error: "灯效 API 未配置，请确认 LIGHT_TEMPLATE_ID 已设置"}
	}
	bizContent, err := BuildLightEffectApnsBody(segments, repeatInput, reason)
	if err != nil {
		return SendResult{OK: false, Error: err.Error()}
	}
	bizUniqueID := newUUID()
	requestBody := map[string]any{
		"appKey": "phone-notifications",
		"bizMap": map[string]any{
			"noticeType": "APP_NOTIFICATION_IMPORTANT",
			"title":      title,
			"reason":     reason,
		},
		"bizUniqueId": bizUniqueID,
		"paramsMap":   map[string]any{"bizContent": bizContent},
		"pushType":    "SPECIFY_PUSH",
		"templateId":  templateID,
	}
	if logger != nil {
		logger.Info(fmt.Sprintf("Light sender: POST %s, bizUniqueId=%s", apiURL, bizUniqueID))
	}
	res, resBody, err := postJSON(apiURL, apiKey, requestBody)
	if err != nil {
		return SendResult{OK: false, Error: err.Error(), Via: "message-service"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return SendResult{OK: false, Status: res.StatusCode, Error: string(resBody), Via: "message-service"}
	}
	var parsed any
	if json.Unmarshal(resBody, &parsed) != nil {
		parsed = string(resBody)
	}
	return SendResult{OK: true, BizUniqueID: bizUniqueID, Response: parsed, Via: "message-service"}
}

func postJSON(apiURL, apiKey string, body map[string]any) (*http.Response, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", stripBearer(apiKey))
	res, err := lightHTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(res.Body)
	return res, resBody, err
}

func parseJSONObject(body []byte) (map[string]any, bool) {
	var parsed map[string]any
	if json.Unmarshal(body, &parsed) != nil || parsed == nil {
		return nil, false
	}
	return parsed, true
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stripBearer(apiKey string) string {
	trimmed := strings.TrimSpace(apiKey)
	if strings.HasPrefix(trimmed, "Bearer ") {
		return strings.TrimPrefix(trimmed, "Bearer ")
	}
	return trimmed
}

func resolveLightTitle(title, reason string, segments []map[string]any) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if r := strings.TrimSpace(reason); r != "" {
		return r
	}
	modes := make([]string, len(segments))
	for i, seg := range segments {
		modes[i], _ = seg["mode"].(string)
	}
	desc := strings.Join(modes, "+")
	if desc == "" {
		desc = "custom"
	}
	return "Effect: " + desc
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func maskKey(apiKey string) string {
	if apiKey == "" {
		return "EMPTY"
	}
	return truncate(apiKey, 20)
}

// newUUID 生成 RFC 4122 v4 UUID。
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
