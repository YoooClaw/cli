package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/envhost"
	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	defaultLongRecordingPollInterval = 2 * time.Second
	defaultLongRecordingMaxPolls     = 3600
	defaultHTTPAttempts              = 3
	defaultHTTPBackoff               = 2 * time.Second
)

var (
	longRecordingRunningStatuses = map[string]bool{"PENDING": true, "RUNNING": true, "SUSPENDED": true}
	longRecordingFailureStatuses = map[string]bool{"FAILED": true, "CANCELED": true, "UNKNOWN": true}
)

// AsrConfig 是录音 ASR 配置。
type AsrConfig struct {
	Mode  string        `json:"mode"`
	API   *AsrAPIConfig `json:"api,omitempty"`
	Local any           `json:"local,omitempty"`
}

// AsrAPIConfig 是 model-proxy 长录音配置。
type AsrAPIConfig struct {
	Provider            string `json:"provider,omitempty"`
	APIKey              string `json:"apiKey,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	Model               string `json:"model,omitempty"`
	Language            string `json:"language,omitempty"`
	EnableNormalization *bool  `json:"enableNormalization,omitempty"`
	UserID              string `json:"userId,omitempty"`
	DeviceID            string `json:"deviceId,omitempty"`
	AppID               string `json:"appId,omitempty"`
	RequestID           string `json:"requestId,omitempty"`
	TraceParent         string `json:"traceParent,omitempty"`
}

// AsrInitResult 是 recordings.asr.init 返回。
type AsrInitResult struct {
	OK            bool   `json:"ok"`
	Mode          string `json:"mode"`
	Provider      string `json:"provider,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	Language      string `json:"language,omitempty"`
	KeyConfigured bool   `json:"keyConfigured,omitempty"`
	Error         string `json:"error,omitempty"`
}

// TranscriptionResult 是 ASR provider 结果。
type TranscriptionResult struct {
	OK          bool                `json:"ok"`
	Text        string              `json:"text,omitempty"`
	Segments    []TranscriptSegment `json:"segments,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	SummaryText string              `json:"summaryText,omitempty"`
	Category    string              `json:"category,omitempty"`
	SourceInfo  TranscriptSource    `json:"sourceInfo,omitempty"`
	RawResponse any                 `json:"rawResponse,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// ValidateAsrConfig 校验 ASR 配置。
func ValidateAsrConfig(cfg *AsrConfig) string {
	if cfg == nil || strings.TrimSpace(cfg.Mode) == "" {
		return "asr.mode is required"
	}
	switch cfg.Mode {
	case "api":
		return ""
	case "local":
		return `本地 Whisper 模式已停用，请改用 asr.mode = "api"`
	case "yoooclaw":
		return "YoooClaw ASR 尚未实现（P2）"
	default:
		return "未知的 ASR mode: " + cfg.Mode
	}
}

// IsAsrConfigured 判断 ASR 是否可用。
func IsAsrConfigured(cfg *AsrConfig) bool {
	return cfg != nil && ValidateAsrConfig(cfg) == ""
}

// LoadLocalAsrConfig 读取 recordings/asr-config.json。
func LoadLocalAsrConfig(recordingsDir string) *AsrConfig {
	var cfg AsrConfig
	exists, err := fsutil.ReadJSON(filepath.Join(recordingsDir, "asr-config.json"), &cfg)
	if err != nil || !exists {
		return nil
	}
	return &cfg
}

// ResolveAsrConfig 优先使用 caller ASR，再回退本地配置；apiKey 为空时注入 account
// fallback，endpoint 为空时按 host 注入默认端点。这里是 ASR 配置的唯一收敛点
// （InitializeAsr 与 TriggerTranscription 都走它），所以主机只需在此落一次。
// host 由调用方从 config.ResolveCloudHost 解析后传入，传空则回落到环境默认值；
// 调用方显式传入的 api.endpoint 优先级最高，不被覆盖。
func ResolveAsrConfig(caller, local *AsrConfig, fallbackAPIKey, host string) *AsrConfig {
	chosen := caller
	if chosen == nil {
		chosen = local
	}
	if chosen == nil || chosen.Mode != "api" {
		return chosen
	}
	out := *chosen
	api := AsrAPIConfig{}
	if chosen.API != nil {
		api = *chosen.API
	}
	if strings.TrimSpace(api.APIKey) == "" {
		api.APIKey = strings.TrimSpace(fallbackAPIKey)
	}
	if strings.TrimSpace(api.Endpoint) == "" {
		api.Endpoint = submitEndpointForHost(host)
	}
	out.API = &api
	return &out
}

// InitializeAsr 显式初始化/校验 ASR。
func InitializeAsr(cfg *AsrConfig) AsrInitResult {
	if errMsg := ValidateAsrConfig(cfg); errMsg != "" {
		mode := ""
		if cfg != nil {
			mode = cfg.Mode
		}
		return AsrInitResult{OK: false, Mode: mode, Error: errMsg}
	}
	endpoint := resolveSubmitEndpoint(cfg.API)
	keyConfigured := strings.TrimSpace(cfg.APIValue().APIKey) != "" || strings.TrimSpace(creds.ResolveAPIKey().Value) != ""
	if !keyConfigured {
		return AsrInitResult{
			OK: false, Mode: "api", Provider: "model-proxy", Endpoint: endpoint,
			Language: firstNonEmpty(cfg.APIValue().Language, "auto"), KeyConfigured: false,
			Error: "API Key 未设置，请在本次 asr.api.apiKey 中传入，或先写入 credentials.json / 执行 yc auth set-api-key <apiKey>",
		}
	}
	return AsrInitResult{
		OK: true, Mode: "api", Provider: "model-proxy", Endpoint: endpoint,
		Language: firstNonEmpty(cfg.APIValue().Language, "auto"), KeyConfigured: true,
	}
}

// APIValue 返回非 nil API 配置副本。
func (c *AsrConfig) APIValue() AsrAPIConfig {
	if c == nil || c.API == nil {
		return AsrAPIConfig{}
	}
	return *c.API
}

// TranscribeAudio 执行 ASR 转写。
func TranscribeAudio(audioFilePath string, cfg AsrConfig, logger Logger, audioOssURL string, audioDurationMS float64) TranscriptionResult {
	if _, err := os.Stat(audioFilePath); err != nil {
		return TranscriptionResult{OK: false, Error: "音频文件不存在: " + audioFilePath}
	}
	logger.Info("[asr] 开始转写: mode=" + cfg.Mode + ", file=" + audioFilePath)
	switch cfg.Mode {
	case "api":
		return transcribeWithModelProxy(audioOssURL, audioDurationMS, cfg.API, logger)
	case "local":
		return TranscriptionResult{OK: false, Error: `本地 Whisper 模式已停用，请改用 asr.mode = "api"`}
	case "yoooclaw":
		return TranscriptionResult{OK: false, Error: "YoooClaw ASR 尚未实现（P2）"}
	default:
		return TranscriptionResult{OK: false, Error: "未知的 ASR mode: " + cfg.Mode}
	}
}

// WorkflowResult 是完整 ASR 工作流落盘结果。
type WorkflowResult struct {
	OK                     bool             `json:"ok"`
	TranscriptFilename     string           `json:"transcriptFilename,omitempty"`
	TranscriptDataFilename string           `json:"transcriptDataFilename,omitempty"`
	SummaryFilename        string           `json:"summaryFilename,omitempty"`
	Transcript             []TranscriptItem `json:"transcript,omitempty"`
	Summary                string           `json:"summary,omitempty"`
	Title                  string           `json:"title,omitempty"`
	Error                  string           `json:"error,omitempty"`
}

// RunTranscriptionWorkflow 转写并写 transcript-data/transcripts/summaries。
func RunTranscriptionWorkflow(storage *Storage, entry Entry, cfg AsrConfig, logger Logger) WorkflowResult {
	audioPath := storage.AudioFilePath(entry.ID, entry.Metadata.OssAudioURL)
	if strings.TrimSpace(entry.AudioFile) != "" {
		audioPath = filepath.Join(storage.dir, entry.AudioFile)
	}
	durationMS := entry.Metadata.DurationSec * 1000
	result := TranscribeAudio(audioPath, cfg, logger, entry.Metadata.OssAudioURL, durationMS)
	if !result.OK {
		return WorkflowResult{OK: false, Error: result.Error}
	}

	title := strings.TrimSpace(result.Summary)
	if title == "" {
		title = extractSummary(result.Text)
	}
	summary := result.SummaryText
	result.Summary = title
	source := result.SourceInfo
	if source.Provider == "" {
		source.Provider = cfg.Mode
		if cfg.Mode == "api" {
			source.Provider = "model-proxy"
		}
	}
	doc := BuildTranscriptDocument(entry.ID, source, title, result.Category, summary, result.Text, result.Segments, result.RawResponse)
	docBytes, _ := json.MarshalIndent(doc, "", "  ")

	transcriptDataFilename := TranscriptDataFilename(entry.ID)
	if err := fsutil.WriteAtomic(filepath.Join(storage.TranscriptDataDir(), transcriptDataFilename), append(docBytes, '\n'), fsutil.ConfigFileMode); err != nil {
		return WorkflowResult{OK: false, Error: err.Error()}
	}
	logger.Info("[asr] 转写 JSON 已写入: " + filepath.Join(storage.TranscriptDataDir(), transcriptDataFilename))

	markdown := BuildTranscriptMarkdown(result, entry.Metadata.Markers, entry.Metadata.Name, entry.Metadata.DurationSec, entry.Metadata.CreatedAt)
	transcriptFilename := TranscriptFilename(entry.ID, title, entry.Metadata.CreatedAt)
	if err := fsutil.WriteAtomic(filepath.Join(storage.TranscriptsDir(), transcriptFilename), []byte(markdown), fsutil.ConfigFileMode); err != nil {
		return WorkflowResult{OK: false, Error: err.Error()}
	}
	logger.Info("[asr] 转写文本已写入: " + filepath.Join(storage.TranscriptsDir(), transcriptFilename))

	summaryFilename := ""
	if strings.TrimSpace(summary) != "" {
		summaryFilename = SummaryFilename(entry.ID, title, entry.Metadata.CreatedAt)
		if err := fsutil.WriteAtomic(filepath.Join(storage.SummariesDir(), summaryFilename), []byte(strings.TrimSpace(summary)), fsutil.ConfigFileMode); err != nil {
			return WorkflowResult{OK: false, Error: err.Error()}
		}
		logger.Info("[asr] 摘要文本已写入: " + filepath.Join(storage.SummariesDir(), summaryFilename))
	}

	return WorkflowResult{
		OK: true, TranscriptFilename: transcriptFilename, TranscriptDataFilename: transcriptDataFilename,
		SummaryFilename: summaryFilename, Transcript: ExtractSourceTextListFromDocument(doc),
		Summary: summary, Title: title,
	}
}

type submitRequest struct {
	AudioOssURL         string `json:"audioOssUrl"`
	Language            string `json:"language,omitempty"`
	EnableNormalization *bool  `json:"enableNormalization,omitempty"`
}

type statusResponse struct {
	TaskID       string        `json:"taskId,omitempty"`
	Status       string        `json:"status,omitempty"`
	RequestID    string        `json:"requestId,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
	RecordResult *recordResult `json:"recordResult,omitempty"`
}

type recordResult struct {
	SourceText     string           `json:"sourceText,omitempty"`
	SourceTextList []sourceTextItem `json:"sourceTextList,omitempty"`
	SummaryResult  string           `json:"summaryResult,omitempty"`
	Category       string           `json:"category,omitempty"`
	Title          string           `json:"title,omitempty"`
}

type sourceTextItem struct {
	Content   string   `json:"content,omitempty"`
	SpeakerID *int     `json:"speakerId,omitempty"`
	StartTime *float64 `json:"startTime,omitempty"`
	EndTime   *float64 `json:"endTime,omitempty"`
}

type responseEnvelope struct {
	Success *bool           `json:"success,omitempty"`
	Code    any             `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func transcribeWithModelProxy(audioOssURL string, audioDurationMS float64, apiConfig *AsrAPIConfig, logger Logger) TranscriptionResult {
	audioOssURL = strings.TrimSpace(audioOssURL)
	if audioOssURL == "" {
		return TranscriptionResult{OK: false, Error: "API 模式缺少 audioOssUrl，无法调用 model-proxy"}
	}
	api := AsrAPIConfig{}
	if apiConfig != nil {
		api = *apiConfig
	}
	apiKey := normalizeAPIKeyHeader(api.APIKey)
	if apiKey == "" {
		apiKey = normalizeAPIKeyHeader(creds.ResolveAPIKey().Value)
	}
	if apiKey == "" {
		return TranscriptionResult{OK: false, Error: "API Key 未设置，无法调用 model-proxy"}
	}
	submitEndpoint := resolveSubmitEndpoint(&api)
	body := submitRequest{AudioOssURL: audioOssURL}
	if strings.TrimSpace(api.Language) != "" {
		body.Language = strings.TrimSpace(api.Language)
	}
	if api.EnableNormalization != nil {
		body.EnableNormalization = api.EnableNormalization
	}
	logger.Info("[asr-submit] 提交长录音任务: endpoint=" + submitEndpoint)

	raw, status, err := doJSONWithRetry(http.MethodPost, submitEndpoint, apiKey, body, logger, "asr-submit")
	if err != nil {
		return TranscriptionResult{OK: false, Error: "Model Proxy ASR submit network error: " + err.Error()}
	}
	if status < 200 || status >= 300 {
		return TranscriptionResult{OK: false, Error: fmt.Sprintf("Model Proxy ASR error: %d %s", status, truncate(string(raw), 200))}
	}
	data, envelopeErr := unwrapStatusResponse(raw)
	if envelopeErr != "" {
		return TranscriptionResult{OK: false, Error: "Model Proxy ASR 提交失败: " + envelopeErr}
	}
	taskID := strings.TrimSpace(data.TaskID)
	requestID := strings.TrimSpace(data.RequestID)
	taskStatus := normalizeTaskStatus(data.Status)
	if taskID == "" {
		return TranscriptionResult{OK: false, Error: "Model Proxy ASR 响应缺少 taskId"}
	}
	logger.Info(fmt.Sprintf("[asr] Model Proxy 长录音任务已提交: taskId=%s, status=%s, requestId=%s", taskID, firstNonEmpty(taskStatus, "UNKNOWN"), firstNonEmpty(requestID, "n/a")))
	if longRecordingFailureStatuses[taskStatus] {
		return TranscriptionResult{OK: false, Error: buildStatusError(data, taskStatus)}
	}
	return pollLongRecordingTask(apiKey, taskID, requestID, audioDurationMS, &api, logger)
}

func pollLongRecordingTask(apiKey, taskID, initialRequestID string, audioDurationMS float64, apiConfig *AsrAPIConfig, logger Logger) TranscriptionResult {
	base := resolveQueryBaseURL(apiConfig)
	pollInterval := pollInterval()
	lastStatus := ""
	for attempt := 1; attempt <= defaultLongRecordingMaxPolls; attempt++ {
		queryURL := strings.TrimRight(base, "/") + "/" + urlPathEscape(taskID)
		raw, statusCode, err := doJSONGet(queryURL, apiKey)
		if err != nil {
			logger.Warn(fmt.Sprintf("[asr-query] 长录音任务查询网络异常: taskId=%s, attempt=%d, error=%s", taskID, attempt, err.Error()))
			if attempt < defaultLongRecordingMaxPolls {
				time.Sleep(pollInterval)
				continue
			}
			return TranscriptionResult{OK: false, Error: "Model Proxy ASR query network error: " + err.Error()}
		}
		if statusCode < 200 || statusCode >= 300 {
			if isRetryableStatus(statusCode) && attempt < defaultLongRecordingMaxPolls {
				logger.Warn(fmt.Sprintf("[asr-query] 长录音任务查询暂时失败: taskId=%s, attempt=%d, status=%d", taskID, attempt, statusCode))
				time.Sleep(pollInterval)
				continue
			}
			return TranscriptionResult{OK: false, Error: fmt.Sprintf("Model Proxy ASR query error: %d %s", statusCode, truncate(string(raw), 200))}
		}
		data, envelopeErr := unwrapStatusResponse(raw)
		if envelopeErr != "" {
			return TranscriptionResult{OK: false, Error: "Model Proxy ASR 查询失败: " + envelopeErr}
		}
		status := firstNonEmpty(normalizeTaskStatus(data.Status), "UNKNOWN")
		requestID := firstNonEmpty(strings.TrimSpace(data.RequestID), initialRequestID)
		if status != lastStatus {
			logger.Info(fmt.Sprintf("[asr] Model Proxy 长录音任务状态: taskId=%s, status=%s, attempt=%d, requestId=%s", taskID, status, attempt, firstNonEmpty(requestID, "n/a")))
			lastStatus = status
		}
		if status == "SUCCEEDED" {
			return buildSuccessResult(taskID, requestID, data, audioDurationMS, logger)
		}
		if longRecordingFailureStatuses[status] {
			return TranscriptionResult{OK: false, Error: buildStatusError(data, status)}
		}
		if !longRecordingRunningStatuses[status] {
			return TranscriptionResult{OK: false, Error: "Model Proxy ASR 返回未知任务状态: " + status}
		}
		if attempt < defaultLongRecordingMaxPolls {
			time.Sleep(pollInterval)
		}
	}
	return TranscriptionResult{OK: false, Error: fmt.Sprintf("Model Proxy ASR 轮询超时: taskId=%s", taskID)}
}

func buildSuccessResult(taskID, requestID string, data statusResponse, audioDurationMS float64, logger Logger) TranscriptionResult {
	text, segments := extractTextAndSegments(data.RecordResult, audioDurationMS)
	sourceText := ""
	summaryText := ""
	title := ""
	category := ""
	if data.RecordResult != nil {
		sourceText = strings.TrimSpace(data.RecordResult.SourceText)
		summaryText = strings.TrimSpace(data.RecordResult.SummaryResult)
		title = strings.TrimSpace(data.RecordResult.Title)
		category = strings.TrimSpace(data.RecordResult.Category)
	}
	if text == "" {
		text = firstNonEmpty(sourceText, summaryText)
		if sourceText == "" && summaryText != "" {
			logger.Warn("[asr] Model Proxy 长录音结果缺少 sourceTextList/sourceText，已回退使用 summaryResult 作为转写文本: taskId=" + taskID)
		}
	}
	logger.Info(fmt.Sprintf("[asr] Model Proxy 长录音转写完成: taskId=%s, requestId=%s, chars=%d", taskID, firstNonEmpty(requestID, "n/a"), len([]rune(text))))
	return TranscriptionResult{
		OK: true, Text: text, Segments: segments, Summary: title, SummaryText: summaryText, Category: category,
		SourceInfo:  TranscriptSource{Provider: "model-proxy", TaskID: taskID, RequestID: requestID, Status: "SUCCEEDED"},
		RawResponse: data,
	}
}

func extractTextAndSegments(result *recordResult, finalFallbackEndMS float64) (string, []TranscriptSegment) {
	if result == nil || len(result.SourceTextList) == 0 {
		return "", nil
	}
	segments := make([]TranscriptSegment, 0, len(result.SourceTextList))
	parts := make([]string, 0, len(result.SourceTextList))
	for i, item := range result.SourceTextList {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		start := float64(0)
		if item.StartTime != nil {
			start = *item.StartTime
		}
		end := start
		if item.EndTime != nil {
			end = *item.EndTime
		} else if i+1 < len(result.SourceTextList) && result.SourceTextList[i+1].StartTime != nil {
			end = *result.SourceTextList[i+1].StartTime
		} else if finalFallbackEndMS >= 0 {
			end = finalFallbackEndMS
		}
		if end < start {
			end = start
		}
		segments = append(segments, TranscriptSegment{
			Text: content, StartMS: &start, EndMS: &end, SpeakerID: item.SpeakerID,
		})
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n"), segments
}

func doJSONWithRetry(method, endpoint, apiKey string, body any, logger Logger, contextName string) ([]byte, int, error) {
	payload, _ := json.Marshal(body)
	var lastErr error
	var lastRaw []byte
	var lastStatus int
	for attempt := 1; attempt <= defaultHTTPAttempts; attempt++ {
		reqCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, method, endpoint, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key-Id", apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp != nil {
			lastStatus = resp.StatusCode
			lastRaw, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		cancel()
		if err == nil && !isRetryableStatus(lastStatus) {
			return lastRaw, lastStatus, nil
		}
		if err != nil {
			lastErr = err
		}
		if attempt < defaultHTTPAttempts {
			logger.Warn(fmt.Sprintf("[%s] request retry %d/%d", contextName, attempt, defaultHTTPAttempts))
			time.Sleep(defaultHTTPBackoff * time.Duration(1<<(attempt-1)))
		}
	}
	if lastErr != nil {
		return lastRaw, lastStatus, lastErr
	}
	return lastRaw, lastStatus, nil
}

func doJSONGet(endpoint, apiKey string) ([]byte, int, error) {
	reqCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Api-Key-Id", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, err
}

func unwrapStatusResponse(raw []byte) (statusResponse, string) {
	var direct statusResponse
	_ = json.Unmarshal(raw, &direct)
	var envelope responseEnvelope
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 {
		return direct, ""
	}
	explicitFailure := envelope.Success != nil && !*envelope.Success
	message := strings.TrimSpace(envelope.Message)
	if !explicitFailure && (message == "" || string(envelope.Data) != "null") {
		var data statusResponse
		if json.Unmarshal(envelope.Data, &data) == nil {
			return data, ""
		}
	}
	code := normalizeCode(envelope.Code)
	if code != "" && message != "" {
		return statusResponse{}, code + " " + message
	}
	if message != "" {
		return statusResponse{}, message
	}
	if code != "" {
		return statusResponse{}, code
	}
	return statusResponse{}, "response envelope indicates failure"
}

func normalizeCode(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return fmt.Sprintf("%.0f", t)
	default:
		return ""
	}
}

func buildStatusError(data statusResponse, status string) string {
	if strings.TrimSpace(data.ErrorMessage) != "" {
		return "Model Proxy ASR " + status + ": " + strings.TrimSpace(data.ErrorMessage)
	}
	return "Model Proxy ASR " + status
}

// submitEndpointForHost 由 host 拼出提交端点；host 为空时回落到环境默认主机。
func submitEndpointForHost(host string) string {
	resolved := envhost.Normalize(host)
	if resolved == "" {
		resolved = envhost.Host()
	}
	return "https://" + resolved + "/api/model-proxy/long-recording/submit-task"
}

// resolveSubmitEndpoint 里的 envhost 回退只对没走 ResolveAsrConfig 的直接调用方生效
// （正常链路上 endpoint 已在 ResolveAsrConfig 注入好）。resolveQueryBaseURL 同理。
func resolveSubmitEndpoint(apiConfig *AsrAPIConfig) string {
	if apiConfig != nil && strings.TrimSpace(apiConfig.Endpoint) != "" {
		return strings.TrimSpace(apiConfig.Endpoint)
	}
	return submitEndpointForHost("")
}

func resolveQueryBaseURL(apiConfig *AsrAPIConfig) string {
	if apiConfig != nil && strings.TrimSpace(apiConfig.Endpoint) != "" {
		endpoint := strings.TrimRight(strings.TrimSpace(apiConfig.Endpoint), "/")
		if strings.HasSuffix(endpoint, "/submit-task") {
			return strings.TrimSuffix(endpoint, "/submit-task") + "/query-task-result"
		}
		return endpoint
	}
	return "https://" + envhost.Host() + "/api/model-proxy/long-recording/query-task-result"
}

func normalizeAPIKeyHeader(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	return strings.TrimPrefix(apiKey, "Bearer ")
}

func normalizeTaskStatus(status string) string {
	return strings.ToUpper(strings.TrimSpace(status))
}

func pollInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("OPENCLAW_ASR_POLL_INTERVAL_MS"))
	if raw == "" {
		return defaultLongRecordingPollInterval
	}
	var ms int64
	if _, err := fmt.Sscanf(raw, "%d", &ms); err == nil && ms >= 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultLongRecordingPollInterval
}

func isRetryableStatus(status int) bool {
	return status == 429 || status >= 500
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func urlPathEscape(s string) string {
	r := strings.NewReplacer(" ", "%20", "/", "%2F", "?", "%3F", "#", "%23")
	return r.Replace(s)
}
