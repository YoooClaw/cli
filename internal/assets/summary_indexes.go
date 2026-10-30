package assets

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clientlabel"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/reminders"
)

func (s *Service) summaryWeb(ctx context.Context, q summaryQuery, r *SummaryResult) *Error {
	var result *Error
	err := s.sources["web-pages"].view(func() error {
		root, err := sourceRoot(s.sources["web-pages"])
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer root.Close()
		raw, missing, e := readIndex(root)
		if e != nil || missing {
			result = e
			return nil
		}
		obj, ok := object(raw)
		if !ok {
			result = readFailed()
			return nil
		}
		pages, ok := obj["pages"].([]any)
		if !ok {
			result = readFailed()
			return nil
		}
		var newest time.Time
		latestHash := ""
		p := newPage()
		for _, value := range pages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m, ok := value.(map[string]any)
			if !ok {
				result = readFailed()
				return nil
			}
			link := str(m, "url")
			if strings.TrimSpace(link) == "" {
				link = str(m, "canonicalUrl")
			}
			if str(m, "urlHash") == "" || strings.TrimSpace(link) == "" {
				result = readFailed()
				return nil
			}
			at, err := time.Parse(time.RFC3339Nano, str(m, "capturedAt"))
			if err != nil {
				p.warn("INVALID_TIMESTAMP_SKIPPED")
				continue
			}
			if at.In(q.zone).Format(time.DateOnly) != q.date {
				continue
			}
			*r.Count++
			hash := str(m, "urlHash")
			if r.Latest == nil || at.After(newest) || at.Equal(newest) && hash > latestHash {
				it := item(digest(s.profile, "web-pages", hash), "", map[string]any{"title": str(m, "title"), "url": link})
				r.Latest, newest, latestHash = &it, at, hash
			}
		}
		r.Warnings = p.Warnings
		return nil
	})
	if err != nil {
		return readFailed()
	}
	return result
}

func (s *Service) summaryReminders(ctx context.Context, q summaryQuery, store *reminders.Storage, r *SummaryResult) *Error {
	if store == nil {
		return readFailed()
	}
	err := store.View(func(idx reminders.Index, _ reminders.State, _ func(string) (reminders.Record, error)) error {
		latestClock, latestID := "", ""
		for _, entry := range idx.Items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Match the existing dueFrom/dueTo contract: all completion states,
			// no due date excluded, no scan of notes/detail partitions.
			if entry.Due == nil || entry.Due.Date != q.date {
				continue
			}
			*r.Count++
			clock := ""
			if entry.Due.Time != nil {
				clock = *entry.Due.Time
			}
			if r.Latest == nil || clock > latestClock || clock == latestClock && entry.RecordID > latestID {
				it := reminderItem(entry, entry.RecordID)
				r.Latest, latestClock, latestID = &it, clock, entry.RecordID
			}
		}
		return nil
	})
	if err != nil {
		return readFailed()
	}
	return nil
}

func (s *Service) summaryRecordings(ctx context.Context, q summaryQuery, store *recording.Storage, scope string, r *SummaryResult) *Error {
	if store == nil {
		return readFailed()
	}
	entries, err := store.ReadIndexSnapshot(ctx)
	if errors.Is(err, recording.ErrSummaryIndexTooLarge) {
		return tooLarge()
	}
	if err != nil {
		return readFailed()
	}
	var newest time.Time
	latestID := ""
	p := newPage()
	for _, entry := range entries {
		if ctx.Err() != nil {
			return readFailed()
		}
		if !clientlabel.Visible(entry.ClientLabel, scope) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, entry.Metadata.CreatedAt)
		if entry.ID == "" || err != nil {
			p.warn("INVALID_TIMESTAMP_SKIPPED")
			continue
		}
		if at.In(q.zone).Format(time.DateOnly) != q.date {
			continue
		}
		*r.Count++
		if r.Latest == nil || at.After(newest) || at.Equal(newest) && entry.ID > latestID {
			data := map[string]any{"recordingId": entry.ID, "name": entry.Metadata.Name, "created_at": entry.Metadata.CreatedAt,
				"duration_sec": entry.Metadata.DurationSec, "duration_display": recording.FormatDurationDisplay(entry.Metadata.DurationSec),
				"transfer_status": entry.Status, "audio_status": entry.AudioStatus,
				"has_audio": entry.AudioFile != "", "has_transcript": entry.TranscriptFile != "", "has_srt": entry.SrtFile != ""}
			it := item(digest(s.profile, "recordings", entry.ID), "", data)
			r.Latest, newest, latestID = &it, at, entry.ID
		}
	}
	r.Warnings = p.Warnings
	return nil
}
