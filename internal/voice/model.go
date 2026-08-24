// Package voice 提供语音输入 App 本地每日 JSONL 历史的只读查询能力。
package voice

import "time"

const historyDirName = "audio-jsonl"

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

// HistoryItem 是一条已经保存的语音输入历史。ID 对应源记录的 voice_id；
// 输入法数据库的数字主键不会成为 CLI 对外契约。
type HistoryItem struct {
	ID                string  `json:"id"`
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

// AppSummary 是查询范围内一个真实桌面 App 身份的历史摘要。
type AppSummary struct {
	AppID        string `json:"app_id"`
	AppName      string `json:"app_name"`
	HistoryCount int64  `json:"history_count"`
	LatestAt     string `json:"latest_at"`
}
