// Package healthdata 保存按业务日期组织的精简健康日统计，并归档兼容旧版原始批次。
package healthdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Batch 是经完整校验的日快照，或兼容旧客户端的原始批次。
type Batch struct {
	daily        *DailyBatch
	line         []byte
	collectedAt  time.Time
	ItemCount    int
	DeletedCount int
}

// CollectedAt 是 v2 快照的采集时间；v1 批次为零值。
func (b Batch) CollectedAt() time.Time { return b.collectedAt }

// Prepare 校验 v2 日期和精简指标结构；旧版批次保留原样进入 legacy。
func Prepare(raw json.RawMessage) (Batch, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Batch{}, fmt.Errorf("body must be a JSON object")
	}
	if string(bytes.TrimSpace(fields["schemaVersion"])) == "2" {
		daily, collected, count, err := prepareDaily(raw)
		if err != nil {
			return Batch{}, err
		}
		return Batch{daily: daily, collectedAt: collected, ItemCount: count}, nil
	}
	if version, exists := fields["schemaVersion"]; exists && string(bytes.TrimSpace(version)) != "1" {
		return Batch{}, fmt.Errorf("unsupported health schemaVersion")
	}
	if _, exists := fields["days"]; exists {
		return Batch{}, fmt.Errorf("daily snapshots require schemaVersion=2")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(fields["items"], &items); err != nil || items == nil {
		return Batch{}, fmt.Errorf("items must be an array of objects")
	}
	for i, item := range items {
		if trimmed := bytes.TrimSpace(item); len(trimmed) == 0 || trimmed[0] != '{' {
			return Batch{}, fmt.Errorf("items[%d] must be an object", i)
		}
	}
	var deleted []*string
	if rawIDs, exists := fields["deletedIds"]; exists {
		if err := json.Unmarshal(rawIDs, &deleted); err != nil || deleted == nil {
			return Batch{}, fmt.Errorf("deletedIds must be an array of strings")
		}
		for i, id := range deleted {
			if id == nil {
				return Batch{}, fmt.Errorf("deletedIds[%d] must be a string", i)
			}
		}
	}
	// Remove only insignificant whitespace so each complete batch occupies one JSONL line.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return Batch{}, err
	}
	return Batch{line: compact.Bytes(), ItemCount: len(items), DeletedCount: len(deleted)}, nil
}
