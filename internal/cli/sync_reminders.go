package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/contextdata"
	"github.com/YoooClaw/cli/internal/daemon"
)

func syncSourceNext(ctx *clictx.Context, source string, scope contextdata.Options, scan bool) (any, error) {
	if source != "reminders" {
		return contextdata.Next(ctx.Paths, source, scope, scan)
	}
	client := daemon.NewClient(ctx.Paths)
	return contextdata.NextWithReader(ctx.Paths, source, scope, scan, func() (contextdata.Dataset, error) {
		return readReminderMemory(client.Request)
	})
}

// Read through the daemon: its storage mutex and revision-checked detail reads
// prevent mixing partitions while a full snapshot is replaced.
func readReminderMemory(request func(string, string, any) (int, any, error)) (contextdata.Dataset, error) {
	out := contextdata.Dataset{Items: []contextdata.Item{}, Dates: []string{}, TimeBasis: "modifiedAt, then createdAt, then source lastCollectedAt; never due date"}
	read := func(path string) (assets.ReminderResponse, error) {
		var r assets.ReminderResponse
		status, body, err := request("GET", path, nil)
		if err != nil {
			return r, err
		}
		if status != 200 {
			return r, fmt.Errorf("reminder snapshot query failed (HTTP %d); retry from a fresh snapshot", status)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return r, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return r, err
		}
		if !r.OK || r.Revision == "" || len(r.Warnings) != 0 {
			return r, fmt.Errorf("invalid reminder response")
		}
		return r, nil
	}
	path := "/assets/reminders?status=all&limit=100"
	revision := ""
	seen := map[string]bool{}
	dates := map[string]bool{}
	for {
		page, err := read(path)
		if err != nil {
			return out, err
		}
		if revision == "" {
			revision = page.Revision
		} else if page.Revision != revision {
			return out, fmt.Errorf("reminder revision changed during pagination")
		}
		out.Connected = len(page.Sources) > 0
		for _, summary := range page.Items {
			id := summary.RecordID
			if _, ok := assets.ReminderRoute("/assets/reminders/" + id); !ok || seen[id] {
				return out, fmt.Errorf("invalid or duplicate reminder identity")
			}
			seen[id] = true
			ref := "/assets/reminders/" + id
			detail, err := read(ref + "?revision=" + url.QueryEscape(revision))
			if err != nil {
				return out, err
			}
			if detail.Revision != revision || len(detail.Items) != 1 || detail.Items[0].RecordID != id {
				return out, fmt.Errorf("inconsistent reminder detail")
			}
			if detail.Items[0].Status != "ok" || len(detail.Items[0].TruncatedFields) != 0 {
				return out, fmt.Errorf("incomplete reminder detail")
			}
			data := detail.Items[0].Data
			date := ""
			for _, key := range []string{"modifiedAt", "createdAt"} {
				if t, err := time.Parse(time.RFC3339Nano, contextdata.Str(data, key)); err == nil {
					date = t.Format(time.DateOnly)
					break
				}
			}
			if date == "" {
				source := contextdata.Str(data, "sourceId")
				t, err := time.Parse(time.RFC3339Nano, page.Sources[source].LastCollectedAt)
				if err != nil {
					return out, fmt.Errorf("reminder lacks source observation time")
				}
				date = t.Format(time.DateOnly)
			}
			dates[date] = true
			out.Items = append(out.Items, contextdata.Item{ID: id, Version: contextdata.Hash(data), Date: date, Data: data, Ref: ref})
		}
		if page.NextCursor == nil {
			break
		}
		path = "/assets/reminders?cursor=" + url.QueryEscape(*page.NextCursor)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		ac, _ := a.Data["isCompleted"].(bool)
		bc, _ := b.Data["isCompleted"].(bool)
		if ac != bc {
			return !ac
		}
		return a.ID < b.ID
	})
	for d := range dates {
		out.Dates = append(out.Dates, d)
	}
	sort.Strings(out.Dates)
	return out, nil
}
