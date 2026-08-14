// Package voice 提供语音输入 App 本地历史数据库的只读查询能力。
package voice

import "time"

const (
	historyView      = "agent_voice_history_v1"
	stableUsageView  = "agent_voice_usage_daily_v1"
	legacyUsageTable = "usage_daily"
	databaseFileName = "voice.sqlite3"
)

// Query 是历史记录的查询条件。From 包含，To 不包含；Limit 为 0 时不限制。
type Query struct {
	From     *time.Time
	To       *time.Time
	App      string
	Status   string
	Language string
	HasAudio bool
	Limit    int
	Keyword  string
}

// HistoryItem 是一条已经保存的语音输入历史。
type HistoryItem struct {
	ID                int64   `json:"id"`
	StartedAt         string  `json:"started_at"`
	EndedAt           string  `json:"ended_at"`
	TimezoneOffsetMin int     `json:"timezone_offset_min"`
	DurationMS        int64   `json:"duration_ms"`
	Platform          string  `json:"platform"`
	AppName           string  `json:"app_name"`
	WindowTitle       *string `json:"window_title"`
	Text              string  `json:"text"`
	Language          *string `json:"language"`
	CharCount         int64   `json:"char_count"`
	ResultStatus      string  `json:"result_status"`
	HasAudio          bool    `json:"has_audio"`
	AudioPath         *string `json:"audio_path"`
}

// AppSummary 是当前数据库中一个用户可见 App 名称的历史摘要。
type AppSummary struct {
	AppName      string `json:"app_name"`
	HistoryCount int64  `json:"history_count"`
	LatestAt     string `json:"latest_at"`
}

// UsageDay 是一个本地自然日的权威用量。
type UsageDay struct {
	LocalDate       string `json:"local_date"`
	SuccessfulCount int64  `json:"successful_count"`
	DurationMS      int64  `json:"duration_ms"`
	CharCount       int64  `json:"char_count"`
	UpdatedAt       string `json:"updated_at"`
}

// UsageTotal 是多个自然日的汇总。
type UsageTotal struct {
	SuccessfulCount int64 `json:"successful_count"`
	DurationMS      int64 `json:"duration_ms"`
	CharCount       int64 `json:"char_count"`
}

func timeWithRecordedOffset(unixMS int64, offsetMinutes int) string {
	location := time.FixedZone("", offsetMinutes*60)
	return time.UnixMilli(unixMS).In(location).Format(time.RFC3339Nano)
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	v := value
	return &v
}
