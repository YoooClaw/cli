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
)

// 后端要求 appKey / templateId 写死在代码里（不再从 env 注入）。
const (
	lightAppKey     = "phone-notifications"
	lightTemplateID = "1990771146010017800"
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

// SendLightEffect 把 segments 编码成线协议负载并 POST 到灯效云 API。
func SendLightEffect(apiKey string, segments []map[string]any, repeatInput RepeatInput, reason, title string, logger Logger) SendResult {
	apiURL := LightAPIURL()
	resolvedTitle := resolveLightTitle(title, reason, segments)

	if logger != nil {
		logger.Info(fmt.Sprintf("Light sender: apiUrl=%s, appKey=%s…, templateId=%s, apiKey=%s, title=%s, reason=%s",
			apiURL, truncate(lightAppKey, 8), lightTemplateID, maskKey(apiKey), resolvedTitle, reason))
	}
	if apiURL == "" {
		return SendResult{OK: false, Error: "灯效 API 未配置，请确认构建时已封装 OPENCLAW_HOST_*"}
	}

	bizContent, err := BuildLightEffectApnsBody(segments, repeatInput, reason)
	if err != nil {
		return SendResult{OK: false, Error: err.Error()}
	}
	bizUniqueID := newUUID()

	requestBody := map[string]any{
		"appKey":      lightAppKey,
		"bizMap":      map[string]any{"noticeType": "APP_NOTIFICATION_IMPORTANT", "title": resolvedTitle, "reason": reason},
		"bizUniqueId": bizUniqueID,
		"paramsMap":   map[string]any{"bizContent": bizContent},
		"pushType":    "SPECIFY_PUSH",
		"templateId":  lightTemplateID,
	}
	payload, _ := json.Marshal(requestBody)
	if logger != nil {
		logger.Info(fmt.Sprintf("Light sender: POST %s, bizUniqueId=%s, body=%s", apiURL, bizUniqueID, truncate(string(payload), 500)))
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
		return SendResult{OK: false, Status: res.StatusCode, Error: string(resBody)}
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
