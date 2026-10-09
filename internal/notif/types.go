// Package notif 提供通知的存储模型、查询匹配与（Phase 2）落盘去重逻辑，
// 对齐 phone-notifications 的 storage.ts / cli ntf-query.ts。
package notif

// StoredNotification 是落盘的通知条目（date-keyed JSONL 文件的一行；
// 旧版是 JSON 数组的元素，只读兼容）。
type StoredNotification struct {
	// ClientLabel 来源客户端 label；旧数据无该字段，查询时视为 legacy。
	ClientLabel string `json:"clientLabel,omitempty"`
	// AppName 包名。
	AppName string `json:"appName"`
	// AppDisplayName 应用显示名（缺省时与 AppName 一致）。
	AppDisplayName string `json:"appDisplayName,omitempty"`
	Title          string `json:"title"`
	Content        string `json:"content"`
	// Timestamp ISO 8601 含时区。
	Timestamp string `json:"timestamp"`
	// SenderName 结构化发送人（主要用于飞书）。
	SenderName string `json:"senderName,omitempty"`
	// ConversationType private | group。
	ConversationType string `json:"conversationType,omitempty"`
	// ConversationName 结构化会话名（主要用于飞书群名）。
	ConversationName string `json:"conversationName,omitempty"`
	// Transfer 只在经 `yoooclaw transfer import` 迁入的条目上出现。
	Transfer *TransferMark `json:"transfer,omitempty"`
}

// MemoryPolicySkipHistory 标记迁入的历史通知不进入通知→记忆的自动同步。
const MemoryPolicySkipHistory = "skip-history"

// TransferMark 记录迁入条目的原始来源（与 phone-notifications 插件字段一致）。
type TransferMark struct {
	Origin       string `json:"origin"`
	RecordID     string `json:"recordId"`
	MemoryPolicy string `json:"memoryPolicy,omitempty"`
}

// NeedsMemory 报告条目是否应进入通知→记忆同步（迁入的历史通知跳过）。
func (n StoredNotification) NeedsMemory() bool {
	return n.Transfer == nil || n.Transfer.MemoryPolicy != MemoryPolicySkipHistory
}

// RawNotification 是手机端上报 / HTTP ingest 的原始通知。
type RawNotification struct {
	ID        string         `json:"id"`
	App       string         `json:"app"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Timestamp string         `json:"timestamp"`
	Category  string         `json:"category,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// PluginConfig 是存储层关心的配置子集。
type PluginConfig struct {
	// RetentionDays 为 nil 表示永久保存。
	RetentionDays *int
	IgnoredApps   []string
}
