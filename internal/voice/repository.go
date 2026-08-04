package voice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	_ "modernc.org/sqlite"
)

var requiredHistoryColumns = []string{
	"id", "started_at_ms", "ended_at_ms", "timezone_offset_min", "duration_ms",
	"platform", "app_id", "app_name", "window_title", "text", "language",
	"char_count", "result_status", "audio_rel_path",
}

var requiredUsageColumns = []string{
	"local_date", "successful_count", "duration_ms", "char_count", "updated_at_ms",
}

// Repository 持有对一个 profile 的 voice.sqlite3 的单连接只读访问。
type Repository struct {
	dir  string
	path string
	db   *sql.DB
}

// Open 以只读 URI 打开数据库，并验证稳定历史视图。不能创建缺失的数据库。
func Open(ctx context.Context, dir string) (*Repository, error) {
	databasePath := filepath.Join(dir, databaseFileName)
	info, err := os.Stat(databasePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errs.New(
			errs.CodeVoiceStorageNotFound,
			"未找到语音输入历史数据库",
			map[string]any{"path": databasePath},
		)
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errs.New(
			errs.CodeStorageUnavailable,
			"语音输入历史数据库不可用",
			map[string]any{"path": databasePath},
		)
	}

	u := readOnlyDatabaseURI(databasePath, runtime.GOOS)
	db, err := sql.Open("sqlite", u)
	if err != nil {
		return nil, storageError(databasePath, "无法打开语音输入历史数据库", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	repository := &Repository{dir: dir, path: databasePath, db: db}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, storageError(databasePath, "无法读取语音输入历史数据库", err)
	}
	if err := repository.ensureColumns(ctx, historyView, requiredHistoryColumns); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repository, nil
}

func readOnlyDatabaseURI(databasePath, goos string) string {
	uriPath := filepath.ToSlash(databasePath)
	if goos == "windows" {
		uriPath = strings.ReplaceAll(uriPath, `\`, "/")
		if len(uriPath) >= 3 && uriPath[1] == ':' && uriPath[2] == '/' {
			uriPath = "/" + uriPath
		}
	}
	u := &url.URL{Scheme: "file", Path: uriPath}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(2000)")
	u.RawQuery = query.Encode()
	return u.String()
}

func storageError(path, message string, cause error) error {
	details := map[string]any{"path": path}
	if cause != nil {
		details["cause"] = cause.Error()
	}
	return errs.New(errs.CodeStorageUnavailable, message, details)
}

// Close 关闭数据库句柄。
func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) ensureColumns(ctx context.Context, object string, required []string) error {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", object))
	if err != nil {
		return storageError(r.path, "无法检查语音输入数据库结构", err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return storageError(r.path, "无法检查语音输入数据库结构", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return storageError(r.path, "无法检查语音输入数据库结构", err)
	}
	var missing []string
	for _, column := range required {
		if !found[column] {
			missing = append(missing, column)
		}
	}
	if len(missing) > 0 {
		return errs.New(
			errs.CodeVoiceSchemaUnsupported,
			"语音输入数据库版本不受支持",
			map[string]any{"object": object, "missing_columns": missing, "path": r.path},
		)
	}
	return nil
}

func appendHistoryFilters(query *strings.Builder, args *[]any, opts Query, includeKeyword bool) {
	if opts.From != nil {
		query.WriteString(" AND started_at_ms >= ?")
		*args = append(*args, opts.From.UnixMilli())
	}
	if opts.To != nil {
		query.WriteString(" AND started_at_ms < ?")
		*args = append(*args, opts.To.UnixMilli())
	}
	if app := strings.TrimSpace(opts.App); app != "" {
		query.WriteString(" AND lower(trim(coalesce(app_name, ''))) = lower(?)")
		*args = append(*args, app)
	}
	if status := strings.TrimSpace(opts.Status); status != "" {
		query.WriteString(" AND lower(trim(coalesce(result_status, ''))) = lower(?)")
		*args = append(*args, status)
	}
	if language := strings.TrimSpace(opts.Language); language != "" {
		query.WriteString(" AND lower(trim(coalesce(language, ''))) = lower(?)")
		*args = append(*args, language)
	}
	if includeKeyword {
		query.WriteString(" AND instr(lower(coalesce(text, '')), lower(?)) > 0")
		*args = append(*args, strings.TrimSpace(opts.Keyword))
	}
}

// List 返回按开始时间倒序排列的历史。Keyword 非空时仅搜索最终上屏文本。
func (r *Repository) List(ctx context.Context, opts Query) ([]HistoryItem, error) {
	var statement strings.Builder
	statement.WriteString(`SELECT id, started_at_ms, ended_at_ms, timezone_offset_min,
duration_ms, platform, app_name, window_title, text, language, char_count,
result_status, audio_rel_path FROM agent_voice_history_v1 WHERE 1 = 1`)
	args := make([]any, 0, 6)
	appendHistoryFilters(&statement, &args, opts, strings.TrimSpace(opts.Keyword) != "")
	statement.WriteString(" ORDER BY started_at_ms DESC, id DESC")

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(r.path, "无法开始语音输入历史只读查询", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, statement.String(), args...)
	if err != nil {
		return nil, storageError(r.path, "无法查询语音输入历史", err)
	}
	defer rows.Close()

	items := make([]HistoryItem, 0)
	for rows.Next() {
		item, audioRelative, err := scanHistory(rows)
		if err != nil {
			return nil, storageError(r.path, "无法读取语音输入历史", err)
		}
		if path, ok := resolveAudioPath(r.dir, audioRelative); ok {
			item.HasAudio = true
			item.AudioPath = &path
		}
		if opts.HasAudio && !item.HasAudio {
			continue
		}
		items = append(items, item)
		if opts.Limit > 0 && len(items) >= opts.Limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(r.path, "无法读取语音输入历史", err)
	}
	if err := rows.Close(); err != nil {
		return nil, storageError(r.path, "无法结束语音输入历史查询", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(r.path, "无法完成语音输入历史查询", err)
	}
	return items, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHistory(row rowScanner) (HistoryItem, string, error) {
	var item HistoryItem
	var startedMS, endedMS int64
	var offsetMinutes int
	var platform, appName, windowTitle, textValue, language, status, audio sql.NullString
	if err := row.Scan(
		&item.ID, &startedMS, &endedMS, &offsetMinutes, &item.DurationMS,
		&platform, &appName, &windowTitle, &textValue, &language, &item.CharCount,
		&status, &audio,
	); err != nil {
		return HistoryItem{}, "", err
	}
	item.StartedAt = timeWithRecordedOffset(startedMS, offsetMinutes)
	item.EndedAt = timeWithRecordedOffset(endedMS, offsetMinutes)
	item.TimezoneOffsetMin = offsetMinutes
	item.Platform = platform.String
	item.AppName = appName.String
	item.WindowTitle = nullableString(windowTitle.String)
	item.Text = textValue.String
	item.Language = nullableString(language.String)
	item.ResultStatus = status.String
	return item, audio.String, nil
}

// Show 返回指定 ID 的单条历史。
func (r *Repository) Show(ctx context.Context, id int64) (*HistoryItem, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, started_at_ms, ended_at_ms, timezone_offset_min,
duration_ms, platform, app_name, window_title, text, language, char_count,
result_status, audio_rel_path FROM agent_voice_history_v1 WHERE id = ?`, id)
	item, audioRelative, err := scanHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errs.New(errs.CodeNotFound, fmt.Sprintf("语音输入历史不存在：%d", id))
	}
	if err != nil {
		return nil, storageError(r.path, "无法读取语音输入历史", err)
	}
	if path, ok := resolveAudioPath(r.dir, audioRelative); ok {
		item.HasAudio = true
		item.AudioPath = &path
	}
	return &item, nil
}

// Apps 返回查询范围内按 app_name 去重后的候选，绝不暴露 app_id。
func (r *Repository) Apps(ctx context.Context, opts Query) ([]AppSummary, error) {
	var statement strings.Builder
	statement.WriteString(`WITH ranked AS (
SELECT trim(app_name) AS app_name, started_at_ms, timezone_offset_min,
COUNT(*) OVER (PARTITION BY lower(trim(app_name))) AS history_count,
ROW_NUMBER() OVER (
  PARTITION BY lower(trim(app_name)) ORDER BY started_at_ms DESC, id DESC
) AS row_rank
FROM agent_voice_history_v1 WHERE trim(coalesce(app_name, '')) <> ''`)
	args := make([]any, 0, 2)
	appendHistoryFilters(&statement, &args, Query{From: opts.From, To: opts.To}, false)
	statement.WriteString(`)
SELECT app_name, history_count, started_at_ms, timezone_offset_min
FROM ranked WHERE row_rank = 1 ORDER BY started_at_ms DESC, app_name COLLATE NOCASE`)

	rows, err := r.db.QueryContext(ctx, statement.String(), args...)
	if err != nil {
		return nil, storageError(r.path, "无法查询语音输入 App 列表", err)
	}
	defer rows.Close()
	apps := make([]AppSummary, 0)
	for rows.Next() {
		var app AppSummary
		var latestMS int64
		var offsetMin int
		if err := rows.Scan(&app.AppName, &app.HistoryCount, &latestMS, &offsetMin); err != nil {
			return nil, storageError(r.path, "无法读取语音输入 App 列表", err)
		}
		app.LatestAt = timeWithRecordedOffset(latestMS, offsetMin)
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(r.path, "无法读取语音输入 App 列表", err)
	}
	return apps, nil
}

func (r *Repository) usageSource(ctx context.Context) (string, error) {
	for _, source := range []string{stableUsageView, legacyUsageTable} {
		var name string
		err := r.db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE (type = 'table' OR type = 'view') AND name = ?",
			source,
		).Scan(&name)
		if err == nil {
			if err := r.ensureColumns(ctx, source, requiredUsageColumns); err != nil {
				return "", err
			}
			return source, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", storageError(r.path, "无法检查语音输入用量表", err)
		}
	}
	return "", errs.New(
		errs.CodeVoiceSchemaUnsupported,
		"语音输入数据库缺少用量统计表",
		map[string]any{"object": stableUsageView + " or " + legacyUsageTable, "path": r.path},
	)
}

// Stats 从权威每日用量表读取数据；from 包含，to 不包含。
func (r *Repository) Stats(ctx context.Context, from, to string) ([]UsageDay, UsageTotal, error) {
	source, err := r.usageSource(ctx)
	if err != nil {
		return nil, UsageTotal{}, err
	}
	statement := fmt.Sprintf(`SELECT local_date, successful_count, duration_ms, char_count, updated_at_ms
FROM %s WHERE 1 = 1`, source)
	args := make([]any, 0, 2)
	if from != "" {
		statement += " AND local_date >= ?"
		args = append(args, from)
	}
	if to != "" {
		statement += " AND local_date < ?"
		args = append(args, to)
	}
	statement += " ORDER BY local_date DESC"
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, UsageTotal{}, storageError(r.path, "无法查询语音输入用量", err)
	}
	defer rows.Close()
	days := make([]UsageDay, 0)
	var total UsageTotal
	for rows.Next() {
		var day UsageDay
		var updatedMS int64
		if err := rows.Scan(&day.LocalDate, &day.SuccessfulCount, &day.DurationMS, &day.CharCount, &updatedMS); err != nil {
			return nil, UsageTotal{}, storageError(r.path, "无法读取语音输入用量", err)
		}
		day.UpdatedAt = time.UnixMilli(updatedMS).In(time.Local).Format(time.RFC3339Nano)
		days = append(days, day)
		total.SuccessfulCount += day.SuccessfulCount
		total.DurationMS += day.DurationMS
		total.CharCount += day.CharCount
	}
	if err := rows.Err(); err != nil {
		return nil, UsageTotal{}, storageError(r.path, "无法读取语音输入用量", err)
	}
	return days, total, nil
}
