package light

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
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
}

// SendLightEffect 把结构化 segments POST 到 Notification Intelligence Service 的
// 插件侧一次性亮灯 Facade（线协议编码与 message-service 调用由服务端完成）。
// host 见 LightAPIURL。
func SendLightEffect(host, apiKey string, segments []map[string]any, repeatInput RepeatInput, reason, title string, logger Logger) SendResult {
	apiURL := LightAPIURL(host)
	resolvedTitle := resolveLightTitle(title, reason, segments)

	if logger != nil {
		logger.Info(fmt.Sprintf("Light sender: apiUrl=%s, apiKey=%s, title=%s, reason=%s",
			apiURL, maskKey(apiKey), resolvedTitle, reason))
	}
	if apiURL == "" {
		return SendResult{OK: false, Error: "灯效 API 未配置，请确认构建时已封装 OPENCLAW_HOST_*"}
	}

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
	bizUniqueID := newUUID()

	requestBody := map[string]any{
		"title":        resolvedTitle,
		"bizUniqueId":  bizUniqueID,
		"repeat_times": repeatTimes,
		"segments":     segments,
	}
	if strings.TrimSpace(reason) != "" {
		requestBody["reason"] = reason
	}
	payload, _ := json.Marshal(requestBody)
	if logger != nil {
		logger.Info(fmt.Sprintf("Light sender: POST %s, bizUniqueId=%s, segments_count=%d, repeat_times=%d",
			apiURL, bizUniqueID, len(segments), repeatTimes))
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return SendResult{OK: false, Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", strings.TrimPrefix(apiKey, "Bearer "))

	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return SendResult{OK: false, Error: err.Error()}
	}
	defer res.Body.Close()
	resBody, _ := io.ReadAll(res.Body)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if logger != nil {
			logger.Warn(fmt.Sprintf("Light sender: FAILED %d, url=%s, resBody=%s", res.StatusCode, apiURL, truncate(string(resBody), 500)))
		}
		return SendResult{OK: false, Status: res.StatusCode, Error: decorateSendError(string(resBody), apiKey)}
	}

	// 响应外壳：{code, msg, date, data:{success, requestId, bizUniqueId, message}}，code=000000 为成功。
	var envelope struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(resBody, &envelope) == nil && envelope.Code != "" && envelope.Code != "000000" {
		if logger != nil {
			logger.Warn(fmt.Sprintf("Light sender: FAILED code=%s, msg=%s, bizUniqueId=%s", envelope.Code, envelope.Msg, bizUniqueID))
		}
		return SendResult{OK: false, Status: res.StatusCode, BizUniqueID: bizUniqueID, Error: envelope.Code + ": " + envelope.Msg}
	}
	if logger != nil {
		logger.Info(fmt.Sprintf("Light sender: OK bizUniqueId=%s, resBody=%s", bizUniqueID, truncate(string(resBody), 200)))
	}
	var parsed any
	if json.Unmarshal(resBody, &parsed) != nil {
		parsed = string(resBody)
	}
	return SendResult{OK: true, BizUniqueID: bizUniqueID, Response: parsed}
}

// decorateSendError 为 “Invalid plugin API Key” 401 补一句排障指引（话术见
// creds.PluginAPIKeyRejectedHint，与灯效规则链路保持一致）。
func decorateSendError(body, apiKey string) string {
	if !strings.Contains(strings.ToLower(body), "invalid plugin api key") {
		return body
	}
	return body + creds.PluginAPIKeyRejectedHint(apiKey)
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
