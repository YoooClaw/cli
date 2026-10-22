package contextdata

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/paths"
)

type Options struct {
	All    bool   `json:"all"`
	Date   string `json:"date,omitempty"`
	From   string `json:"fromDate,omitempty"`
	To     string `json:"toDate,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Metric string `json:"metric,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type cursor struct {
	Offset int
	Digest string
	AsOf   time.Time
}

func (o Options) Validate() error {
	if o.All && (o.Date != "" || o.From != "" || o.To != "") {
		return fmt.Errorf("--all conflicts with date ranges")
	}
	if o.Date != "" && (o.From != "" || o.To != "") {
		return fmt.Errorf("--date conflicts with date ranges")
	}
	for _, s := range []string{o.Date, o.From, o.To} {
		if s != "" && !Date(s) {
			return fmt.Errorf("dates must use YYYY-MM-DD")
		}
	}
	if o.From != "" && o.To != "" && o.From > o.To {
		return fmt.Errorf("from-date must not exceed to-date")
	}
	return nil
}
func (o Options) Includes(d string) bool {
	if o.Date != "" {
		return d == o.Date
	}
	return (o.From == "" || d >= o.From) && (o.To == "" || d <= o.To)
}
func Query(p paths.Paths, source, mode, id string, o Options, now time.Time) (map[string]any, error) {
	if e := o.Validate(); e != nil {
		return nil, e
	}
	if o.Limit < 1 || o.Limit > 500 {
		return nil, fmt.Errorf("limit must be 1..500")
	}
	d, e := Read(p, source)
	if e != nil {
		return nil, e
	}
	c := cursor{AsOf: now}
	if o.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(o.Cursor)
		if e != nil {
			return nil, fmt.Errorf("invalid cursor")
		}
		if e = json.Unmarshal(raw, &c); e != nil || c.Offset < 0 || c.AsOf.IsZero() {
			return nil, fmt.Errorf("invalid cursor")
		}
	}
	scope := "all_synced"
	desc := "全部已同步数据；不代表设备完整历史"
	if o.Date != "" || o.From != "" || o.To != "" {
		scope = "date_range"
		desc = "显式日期范围内的已同步数据"
	} else if source == "calendars" && !o.All && mode == "list" {
		scope = "upcoming"
		desc = "默认仅返回开始时间晚于查询时刻的已同步日程"
	}
	items := []Item{}
	for _, v := range d.Items {
		if mode == "get" && id != "" && v.ID != id {
			continue
		}
		include := o.Includes(v.Date)
		if source == "calendars" && scope == "date_range" {
			// Calendar ranges select overlapping source-local days, including an event
			// whose start lies before the requested date.
			end, err := time.Parse(time.RFC3339Nano, Str(v.Data, "endAt"))
			if err != nil {
				return nil, err
			}
			start, _ := time.Parse(time.RFC3339Nano, Str(v.Data, "startAt"))
			zone, err := time.LoadLocation(Str(v.Data, "timeZone"))
			if err != nil {
				return nil, err
			}
			if end.After(start) {
				end = end.Add(-time.Nanosecond)
			}
			from, to := o.From, o.To
			if o.Date != "" {
				from = o.Date
				to = o.Date
			}
			include = (to == "" || v.Date <= to) && (from == "" || end.In(zone).Format(time.DateOnly) >= from)
		}
		if !include {
			continue
		}
		if scope == "upcoming" {
			start, _ := calendarStart(v.Data)
			if !start.After(c.AsOf) {
				continue
			}
		}
		if source == "health-data" {
			h := v.Data["header"].(map[string]any)
			zone, err := time.LoadLocation(Str(h, "timeZone"))
			if err != nil {
				return nil, err
			}
			v.Data["partialDay"] = v.Date == c.AsOf.In(zone).Format(time.DateOnly)
			if mode == "series" {
				var metric any
				rs := v.Data["items"].([]map[string]any)
				for _, r := range rs {
					if Str(r, "metric") == o.Metric {
						metric = r
						break
					}
				}
				v.Data = map[string]any{"header": h, "metric": o.Metric, "measurement": metric, "missing": metric == nil, "partialDay": v.Data["partialDay"]}
			}
		}
		if source == "health-data" && mode == "list" {
			v.Data = healthSummary(v.Data)
		}
		items = append(items, v)
	}
	if mode == "get" && len(items) == 0 {
		return nil, fmt.Errorf("record not found in synchronized data")
	}
	digest := Hash(struct {
		Source, Mode, ID, Scope string
		Options                 Options
		Items                   []Item
	}{source, mode, id, scope, Options{All: o.All, Date: o.Date, From: o.From, To: o.To, Metric: o.Metric}, items})
	if o.Cursor != "" && c.Digest != digest {
		return nil, fmt.Errorf("source or query changed; restart pagination")
	}
	if c.Offset > len(items) {
		return nil, fmt.Errorf("cursor out of range")
	}
	end := c.Offset + o.Limit
	if end > len(items) {
		end = len(items)
	}
	var next any
	if end < len(items) {
		b, _ := json.Marshal(cursor{Offset: end, Digest: digest, AsOf: c.AsOf})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return map[string]any{"ok": true, "source": source, "scope": scope, "scopeDescription": desc, "asOf": c.AsOf.Format(time.RFC3339Nano), "timeBasis": d.TimeBasis, "coverage": map[string]any{"connected": d.Connected, "availableDates": d.Dates, "completeDeviceHistory": false}, "items": items[c.Offset:end], "total": len(items), "returned": end - c.Offset, "nextCursor": next}, nil
}
func ParseLimit(s string) (int, error) {
	n, e := strconv.Atoi(s)
	if e != nil || n < 1 || n > 500 {
		return 0, fmt.Errorf("limit must be 1..500")
	}
	return n, nil
}
