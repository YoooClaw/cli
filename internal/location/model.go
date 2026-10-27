// Package location 接收位置记录并按 CLI 本地接收日期追加保存 JSONL。
package location

import (
	"encoding/json"
	"fmt"
)

// Record 原样保留客户端字段；CLI 只检查经纬度，不补充元数据。
// deviceType 为上报设备的可选字符串，允许省略、null 或空字符串。
type Record map[string]json.RawMessage

// Payload 是 POST /locations 的请求体。
type Payload struct {
	Items []Record `json:"items"`
}

// Validate 只要求经纬度是有效范围内的 JSON 数值，允许零值。
func (r Record) Validate() error {
	for _, field := range []struct {
		name  string
		limit float64
	}{
		{"longitude", 180},
		{"latitude", 90},
	} {
		var value *float64
		if err := json.Unmarshal(r[field.name], &value); err != nil || value == nil {
			return fmt.Errorf("%s must be a number", field.name)
		}
		if *value < -field.limit || *value > field.limit {
			return fmt.Errorf("%s must be between %g and %g", field.name, -field.limit, field.limit)
		}
	}
	return nil
}
