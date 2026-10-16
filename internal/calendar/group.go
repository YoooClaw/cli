package calendar

import (
	"encoding/json"
	"fmt"
	"time"
	_ "time/tzdata" // 发布后的独立 CLI 在没有系统时区库时也能解析 IANA 时区。
)

// Prepare 先验证整批并生成每天完整的文件内容，包括没有日程的空日期。
func Prepare(p Payload) (*Batch, error) {
	start, err := parseDate(p.StartDate)
	if err != nil {
		return nil, fmt.Errorf("startDate: %w", err)
	}
	end, err := parseDate(p.EndDate)
	if err != nil {
		return nil, fmt.Errorf("endDate: %w", err)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("endDate must not be before startDate")
	}
	if p.Items == nil {
		return nil, fmt.Errorf("items must be an array; use [] for an empty snapshot")
	}

	batch := &Batch{}
	indices := make(map[string]int)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		indices[date] = len(batch.files)
		batch.files = append(batch.files, dayFile{date: date})
	}
	for i, item := range p.Items {
		first, last, err := recordDates(item)
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		line, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		line = append(line, '\n')
		if first.Before(start) {
			first = start
		}
		if last.After(end) {
			last = end
		}
		if first.After(last) {
			continue
		}
		batch.items++
		for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
			index := indices[day.Format(time.DateOnly)]
			batch.files[index].data = append(batch.files[index].data, line...)
		}
	}
	return batch, nil
}

func parseDate(value string) (time.Time, error) {
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || date.Format(time.DateOnly) != value {
		return time.Time{}, fmt.Errorf("must be a date in YYYY-MM-DD format")
	}
	return date, nil
}

func recordDates(r Record) (time.Time, time.Time, error) {
	var fields struct {
		StartAt  string `json:"startAt"`
		EndAt    string `json:"endAt"`
		TimeZone string `json:"timeZone"`
	}
	for key, target := range map[string]*string{
		"startAt": &fields.StartAt, "endAt": &fields.EndAt, "timeZone": &fields.TimeZone,
	} {
		if err := json.Unmarshal(r[key], target); err != nil || *target == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("%s must be a non-empty string", key)
		}
	}
	zone, err := time.LoadLocation(fields.TimeZone)
	if err != nil || fields.TimeZone == "Local" {
		return time.Time{}, time.Time{}, fmt.Errorf("timeZone must be an IANA time zone")
	}
	start, err := time.Parse(time.RFC3339Nano, fields.StartAt)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("startAt must include an RFC3339 time zone offset")
	}
	end, err := time.Parse(time.RFC3339Nano, fields.EndAt)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("endAt must include an RFC3339 time zone offset")
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("endAt must not be before startAt")
	}
	for _, t := range []time.Time{start, end} {
		_, offset := t.Zone()
		_, expected := t.In(zone).Zone()
		if offset != expected {
			return time.Time{}, time.Time{}, fmt.Errorf("startAt and endAt offsets must match timeZone")
		}
	}
	// 结束点不包含：次日零点结束的日程只写到前一天。
	last := end
	if end.After(start) {
		last = end.Add(-time.Nanosecond)
	}
	return civilDate(start.In(zone)), civilDate(last.In(zone)), nil
}

// 用 UTC 承载纯日期，逐日分组不依赖 CLI 主机时区或夏令时的一天长度。
func civilDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
