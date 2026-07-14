package notif

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

var dateDirRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

const (
	idIndexDirName         = ".ids"
	contentKeyIndexDirName = ".keys"
	unitSep                = "\x1f"
	storedFileMode         = 0o644
)

// Logger 是存储层依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
}

// IngestResult 是一次 ingest 的统计 + 新插入条目。
type IngestResult struct {
	Received         int `json:"received"`
	Ingested         int `json:"ingested"`
	DedupedByID      int `json:"dedupedById"`
	DedupedByContent int `json:"dedupedByContent"`
	Invalid          int `json:"invalid"`
	// Failed 是落盘失败的条数。这些通知既没入库也没记索引，重发可以重新入库。
	Failed   int                  `json:"failed"`
	Inserted []StoredNotification `json:"-"`
}

// Storage 是 date-keyed JSON 通知存储（含 id / content-key 双重去重）。
type Storage struct {
	dir            string
	idIndexDir     string
	contentKeyDir  string
	cfg            PluginConfig
	logger         Logger
	mu             sync.Mutex
	idCache        map[string]map[string]bool
	contentKeyCach map[string]map[string]bool
	dayCache       map[string][]StoredNotification
}

// NewStorage 构造存储；dir 为 notifications 目录。
func NewStorage(dir string, cfg PluginConfig, logger Logger) *Storage {
	return &Storage{
		dir:            dir,
		idIndexDir:     filepath.Join(dir, idIndexDirName),
		contentKeyDir:  filepath.Join(dir, contentKeyIndexDirName),
		cfg:            cfg,
		logger:         logger,
		idCache:        map[string]map[string]bool{},
		contentKeyCach: map[string]map[string]bool{},
		dayCache:       map[string][]StoredNotification{},
	}
}

func (s *Storage) dayPath(dateKey string) string {
	return filepath.Join(s.dir, dateKey+".json")
}

// loadDay 读取当天已落盘的条目。
//
// 当天文件损坏时（例如旧版本非原子写被中断留下的半截 JSON）不能当作「空的一天」
// 静默继续——那样下一次写入会把残骸连同它代表的数据一起覆盖掉。这里把损坏文件
// 隔离备份，并丢弃当天的 id / content-key 索引：索引描述的是已经读不出来的数据，
// 留着它只会让手机重发的同一批通知被判定为重复，永远回不来。
func (s *Storage) loadDay(dateKey string) []StoredNotification {
	if entries, ok := s.dayCache[dateKey]; ok {
		return entries
	}
	filePath := s.dayPath(dateKey)
	entries, err := readStoredFile(filePath)
	if err != nil {
		s.quarantineDay(dateKey, filePath, err)
		entries = nil
	}
	s.dayCache[dateKey] = entries
	return entries
}

func (s *Storage) quarantineDay(dateKey, filePath string, cause error) {
	backup := filePath + ".corrupt-" + time.Now().Format("20060102T150405")
	if err := os.Rename(filePath, backup); err != nil {
		s.logger.Warn("当天通知文件损坏且隔离失败: " + dateKey + ", " + err.Error())
	} else {
		s.logger.Warn("当天通知文件损坏，已隔离为 " + filepath.Base(backup) + "（" + cause.Error() + "）")
	}
	_ = os.Remove(filepath.Join(s.idIndexDir, dateKey+".ids"))
	_ = os.Remove(filepath.Join(s.contentKeyDir, dateKey+".keys"))
	delete(s.idCache, dateKey)
	delete(s.contentKeyCach, dateKey)
}

// Init 准备目录；content-key 索引每次启动重建。
func (s *Storage) Init() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.idIndexDir, 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(s.contentKeyDir)
	return os.MkdirAll(s.contentKeyDir, 0o755)
}

// staged 是一天里待落盘的新条目，以及为它们在内存缓存中占位的索引键。
// 索引键先入缓存（这样同一批里的重复能被挡掉），落盘成功后才写进索引文件；
// 落盘失败则从缓存回滚，避免索引比数据先一步存在。
type staged struct {
	entries    []StoredNotification
	idKeys     []string
	contentKys []string
}

// Ingest 写入一批通知，去重后返回统计与新插入条目。clientLabel 为来源（缺省 "default"）。
//
// 按日期分组、每天只落盘一次：逐条覆写整个当天数组会带来 O(n²) 的写放大
// （一批 800 条能为一个 241 KB 的文件写掉近 100 MB）。
func (s *Storage) Ingest(items []RawNotification, clientLabel string) IngestResult {
	if clientLabel == "" {
		clientLabel = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result := IngestResult{Received: len(items)}
	days := map[string]*staged{}
	var dayOrder []string
	// 记录每条新条目属于哪一天，好在落盘后按原始顺序还原 Inserted。
	type placed struct {
		dateKey string
		entry   StoredNotification
	}
	var placedItems []placed

	for _, n := range items {
		ts, ok := ParseTime(n.Timestamp)
		if !ok {
			s.logger.Warn("忽略非法 timestamp 的通知: " + n.ID)
			result.Invalid++
			continue
		}
		dateKey := ts.In(time.Local).Format("2006-01-02")
		// 先加载当天数据：损坏的文件在这里被隔离，索引也一并重置，
		// 这样后面的 hasID 不会拿着一份「描述已不存在数据」的索引去挡掉重发。
		existing := s.loadDay(dateKey)

		entry := s.buildStored(n, clientLabel)
		if entry.AppDisplayName == "" {
			entry.AppDisplayName = entry.AppName
		}
		label := labelOrLegacy(entry.ClientLabel)
		normalizedID := strings.TrimSpace(n.ID)

		if normalizedID != "" && s.hasID(dateKey, label, normalizedID) {
			result.DedupedByID++
			continue
		}
		if s.hasContentKey(dateKey, existing, entry) {
			result.DedupedByContent++
			continue
		}

		day, ok := days[dateKey]
		if !ok {
			day = &staged{}
			days[dateKey] = day
			dayOrder = append(dayOrder, dateKey)
		}
		day.entries = append(day.entries, entry)
		placedItems = append(placedItems, placed{dateKey: dateKey, entry: entry})

		// 占位到内存缓存，挡掉同一批内部的重复。
		if normalizedID != "" {
			key := s.idKey(label, normalizedID)
			s.idSet(dateKey)[key] = true
			day.idKeys = append(day.idKeys, key)
		}
		ck := contentKey(entry)
		s.contentKeySet(dateKey, existing)[ck] = true
		day.contentKys = append(day.contentKys, ck)
	}

	failedDays := map[string]bool{}
	for _, dateKey := range dayOrder {
		if err := s.flushDay(dateKey, days[dateKey]); err != nil {
			failedDays[dateKey] = true
			s.logger.Warn("当天通知落盘失败，本批不计入库存: " + dateKey + ", " + err.Error())
		}
	}

	for _, p := range placedItems {
		if failedDays[p.dateKey] {
			result.Failed++
			continue
		}
		result.Ingested++
		result.Inserted = append(result.Inserted, p.entry)
	}

	s.prune()
	return result
}

// flushDay 把一天的新条目一次性原子写入，成功后才把索引落盘。
func (s *Storage) flushDay(dateKey string, day *staged) error {
	if len(day.entries) == 0 {
		return nil
	}
	filePath := s.dayPath(dateKey)
	arr := append(s.loadDay(dateKey), day.entries...)

	data, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		s.revertStaged(dateKey, day)
		return err
	}
	// 原子写：os.WriteFile 先截断再写，中途崩溃会留下半截 JSON，
	// 之后解析失败就会把当天数据整份丢掉。
	if err := fsutil.WriteAtomic(filePath, data, storedFileMode); err != nil {
		s.revertStaged(dateKey, day)
		return err
	}
	s.dayCache[dateKey] = arr

	for _, key := range day.idKeys {
		appendLine(filepath.Join(s.idIndexDir, dateKey+".ids"), key)
	}
	for _, key := range day.contentKys {
		appendLine(filepath.Join(s.contentKeyDir, dateKey+".keys"), key)
	}
	return nil
}

// revertStaged 撤回落盘失败那批在内存缓存里的索引占位，否则这些通知会被
// 永久判定为「已存在」，重发也进不来。
func (s *Storage) revertStaged(dateKey string, day *staged) {
	if set, ok := s.idCache[dateKey]; ok {
		for _, key := range day.idKeys {
			delete(set, key)
		}
	}
	if set, ok := s.contentKeyCach[dateKey]; ok {
		for _, key := range day.contentKys {
			delete(set, key)
		}
	}
}

func (s *Storage) buildStored(n RawNotification, clientLabel string) StoredNotification {
	appName := n.App
	if appName == "" {
		appName = "Unknown"
	}
	if feishu, ok := normalizeFeishuFields(n); ok {
		content := feishu.Content
		if content == "" {
			content = buildFallbackContent(n)
		}
		return StoredNotification{
			ClientLabel: clientLabel, AppName: appName, Title: feishu.Title, Content: content,
			Timestamp: n.Timestamp, SenderName: feishu.Structured.SenderName,
			ConversationType: feishu.Structured.ConversationType, ConversationName: feishu.Structured.ConversationName,
		}
	}
	return StoredNotification{
		ClientLabel: clientLabel, AppName: appName, Title: n.Title,
		Content: buildFallbackContent(n), Timestamp: n.Timestamp,
	}
}

func buildFallbackContent(n RawNotification) string {
	if body := strings.TrimSpace(n.Body); body != "" {
		return body
	}
	var parts []string
	if n.Category != "" {
		parts = append(parts, "category:"+n.Category)
	}
	if len(n.Metadata) > 0 {
		if b, err := json.Marshal(n.Metadata); err == nil {
			parts = append(parts, "metadata:"+string(b))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ; ")
}

func labelOrLegacy(label string) string {
	if label == "" {
		return "legacy"
	}
	return label
}

func usesLegacyIDKey(label string) bool {
	return label == "legacy" || label == "default"
}

func (s *Storage) idKey(label, id string) string {
	if usesLegacyIDKey(label) {
		return id
	}
	return label + unitSep + id
}

func (s *Storage) idSet(dateKey string) map[string]bool {
	if set, ok := s.idCache[dateKey]; ok {
		return set
	}
	set := map[string]bool{}
	if raw, err := os.ReadFile(filepath.Join(s.idIndexDir, dateKey+".ids")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if id := strings.TrimSpace(line); id != "" {
				set[id] = true
			}
		}
	}
	s.idCache[dateKey] = set
	return set
}

func (s *Storage) hasID(dateKey, label, id string) bool {
	set := s.idSet(dateKey)
	if set[s.idKey(label, id)] {
		return true
	}
	return usesLegacyIDKey(label) && set[id]
}

func contentKey(e StoredNotification) string {
	h := sha256.New()
	h.Write([]byte(labelOrLegacy(e.ClientLabel)))
	h.Write([]byte(unitSep))
	h.Write([]byte(e.AppName))
	h.Write([]byte(unitSep))
	h.Write([]byte(e.Title))
	h.Write([]byte(unitSep))
	h.Write([]byte(e.Content))
	h.Write([]byte(unitSep))
	h.Write([]byte(e.Timestamp))
	return hex.EncodeToString(h.Sum(nil))
}

// contentKeySet 从当天已落盘的条目重建 content-key 索引（Init 时清空 .keys 目录，
// 所以这份索引始终派生自数据本身，不会比数据活得更久）。
func (s *Storage) contentKeySet(dateKey string, existing []StoredNotification) map[string]bool {
	if set, ok := s.contentKeyCach[dateKey]; ok {
		return set
	}
	set := map[string]bool{}
	keyPath := filepath.Join(s.contentKeyDir, dateKey+".keys")
	for _, item := range existing {
		set[contentKey(item)] = true
	}
	if len(set) > 0 {
		var b strings.Builder
		for k := range set {
			b.WriteString(k)
			b.WriteString("\n")
		}
		_ = fsutil.WriteAtomic(keyPath, []byte(b.String()), storedFileMode)
	} else {
		_ = os.Remove(keyPath)
	}
	s.contentKeyCach[dateKey] = set
	return set
}

func (s *Storage) hasContentKey(dateKey string, existing []StoredNotification, e StoredNotification) bool {
	set := s.contentKeySet(dateKey, existing)
	for _, label := range contentKeyLabels(e.ClientLabel) {
		probe := e
		probe.ClientLabel = label
		if set[contentKey(probe)] {
			return true
		}
	}
	return false
}

func contentKeyLabels(label string) []string {
	l := labelOrLegacy(label)
	if l == "default" {
		return []string{"default", "legacy"}
	}
	return []string{l}
}

func (s *Storage) prune() {
	if s.cfg.RetentionDays == nil {
		return
	}
	cutoff := time.Now().Add(-time.Duration(*s.cfg.RetentionDays) * 24 * time.Hour).In(time.Local).Format("2006-01-02")
	pruneByDate(s.dir, cutoff, []string{".json", ".md"}, func(k string) {
		delete(s.idCache, k)
		delete(s.contentKeyCach, k)
		delete(s.dayCache, k)
	}, false)
	pruneByDate(s.idIndexDir, cutoff, []string{".ids"}, func(k string) { delete(s.idCache, k) }, true)
	pruneByDate(s.contentKeyDir, cutoff, []string{".keys"}, func(k string) { delete(s.contentKeyCach, k) }, true)
}

func pruneByDate(dir, cutoff string, exts []string, evict func(string), filesOnly bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if !filesOnly && len(name) == 10 && name < cutoff && dateDirRE.MatchString(name) {
				_ = os.RemoveAll(filepath.Join(dir, name))
			}
			continue
		}
		for _, ext := range exts {
			if strings.HasSuffix(name, ext) {
				key := strings.TrimSuffix(name, ext)
				if len(key) == 10 && key < cutoff {
					_ = os.Remove(filepath.Join(dir, name))
					evict(key)
				}
				break
			}
		}
	}
}

// readStoredFile 读取当天条目。文件不存在返回空；内容损坏返回错误，
// 交由调用方隔离——静默当成空的一天会让下一次写入覆盖掉当天全部数据。
func readStoredFile(filePath string) ([]StoredNotification, error) {
	raw, err := os.ReadFile(filePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var items []StoredNotification
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
