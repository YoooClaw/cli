package notif

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Rebuild streams the requested day only. It does not populate dayCache, invoke
// quarantine/migration, or scan any other day. Legacy arrays are strict; broken
// JSONL rows are skipped, including torn tails. Valid unterminated rows count.
func rebuildDailyStats(ctx context.Context, path string, expected os.FileInfo, idx *dailyStatsIndex) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(expected, before) || statsSource(before) != idx.Source {
		return errors.New("notification source changed before stats rebuild")
	}
	raw := statsContextReader{ctx: ctx, reader: f}
	if strings.HasSuffix(path, ".jsonl") {
		r := bufio.NewReader(raw)
		for {
			line, readErr := r.ReadBytes('\n')
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			idx.addRaw(line)
			if readErr == io.EOF {
				break
			}
		}
	} else if err := rebuildLegacyStats(raw, idx); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || statsSource(after) != idx.Source {
		return errors.New("notification source changed during stats rebuild")
	}
	return nil
}

type statsContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r statsContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func rebuildLegacyStats(r io.Reader, idx *dailyStatsIndex) error {
	d := json.NewDecoder(r)
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('[') {
		return errors.New("legacy notification source must be an array")
	}
	for d.More() {
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		idx.addRaw(raw)
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("invalid trailing legacy notification data")
	}
	return nil
}

func (idx *dailyStatsIndex) addRaw(raw []byte) {
	// Match the asset reader's required fields. Wrong optional field types do
	// not hide otherwise valid notifications; invalid UTF-8/JSON is not counted.
	if !utf8.Valid(raw) {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return
	}
	str := func(key string) string {
		var value string
		_ = json.Unmarshal(fields[key], &value)
		return value
	}
	entry := StoredNotification{
		AppName: str("appName"), AppDisplayName: str("appDisplayName"),
		Title: str("title"), Content: str("content"), Timestamp: str("timestamp"),
		ClientLabel: str("clientLabel"), SenderName: str("senderName"),
		ConversationType: str("conversationType"), ConversationName: str("conversationName"),
	}
	if mark := fields["transfer"]; len(mark) != 0 && !bytes.Equal(bytes.TrimSpace(mark), []byte("null")) {
		var transfer TransferMark
		if json.Unmarshal(mark, &transfer) == nil {
			entry.Transfer = &transfer
		}
	}
	idx.add(entry)
}
