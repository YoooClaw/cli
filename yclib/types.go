package yclib

import (
	"github.com/YoooClaw/cli/internal/image"
	"github.com/YoooClaw/cli/internal/lightrule"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
)

// 类型 re-export：让消费方完全经 yclib 包消费入参/出参，无需 import internal/...
// 这些是类型别名（identical types），与底层类型可互换传递。
type (
	// RawQueryOpts 是通知查询的原始字符串过滤参数（与 CLI flag 同语义）。
	RawQueryOpts = notif.RawQueryOpts
	// StoredNotification 是一条落盘通知记录。
	StoredNotification = notif.StoredNotification
	// ScanStats 是一次查询扫描的统计。
	ScanStats = notif.ScanStats
	// Count 是一个聚合计数项（维度取值 + 次数）。
	Count = notif.Count

	// ImageEntry 是一条落盘图片记录。
	ImageEntry = image.Entry
	// RecordingEntry 是一条落盘录音记录。
	RecordingEntry = recording.Entry
	// LightRule 是单条灯效规则元数据。
	LightRule = lightrule.Meta
	// TunnelInfo 是单条 Relay 隧道状态。
	TunnelInfo = relay.TunnelStatus
)
