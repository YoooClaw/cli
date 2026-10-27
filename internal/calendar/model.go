// Package calendar 接收完整日程列表，按日分组覆盖 JSONL，保留范围外历史。
package calendar

import (
	"encoding/json"
	"time"
)

// Record 原样保存客户端字段；仅解析日期分组需要的起止时间和时区。
type Record map[string]json.RawMessage

// Payload 是 POST /calendars 的完整快照。首尾日期均包含在覆盖范围内。
// items 必须显式提供；空数组表示该范围内没有日程。
// collectedAt 是本次采集时间，用于拒绝迟到的旧快照。
type Payload struct {
	StartDate   string   `json:"startDate"`
	EndDate     string   `json:"endDate"`
	CollectedAt string   `json:"collectedAt"`
	Items       []Record `json:"items"`
}

type dayFile struct {
	date string
	data []byte
}

// Batch 已完成整批解析与 JSONL 组包，写文件前不再解析日程。
type Batch struct {
	files       []dayFile
	items       int
	collectedAt time.Time
}

func (b *Batch) ItemCount() int { return b.items }
func (b *Batch) DayCount() int  { return len(b.files) }

// CollectedAt 是客户端声明的采集时间，用于和上次成功应用的快照比较新旧。
func (b *Batch) CollectedAt() time.Time { return b.collectedAt }
