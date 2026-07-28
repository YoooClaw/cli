package notif

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	storedFileMode         = fsutil.SecretFileMode
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

// Storage 是 date-keyed JSONL 通知存储（含 id / content-key 双重去重）。
// 文件格式见 dayfile.go：每天一个 .jsonl 追加写；旧 .json 数组只读兼容 + 懒迁移。
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
	return dayFilePath(s.dir, dateKey)
}

// loadDay 读取当天已落盘的条目（带内存缓存）。
func (s *Storage) loadDay(dateKey string) []StoredNotification {
	if entries, ok := s.dayCache[dateKey]; ok {
		return entries
	}
	entries := s.loadDayFromDisk(dateKey)
	s.dayCache[dateKey] = entries
	return entries
}

// loadDayFromDisk 读取当天数据，.jsonl 优先，缺失时回退旧 .json 数组。
//
// .jsonl 逐行解析、坏行跳过：追加写永不覆盖已有数据，坏行（torn write 的半行）
// 最多损失那一条，不需要隔离整天。旧 .json 数组则相反——半截 JSON 解析失败时
// 不能当作「空的一天」静默继续，那样迁移写 .jsonl 时会把残骸代表的数据一起丢掉，
// 所以保留隔离逻辑：备份损坏文件并重置当天索引，让手机重发的同批通知能重新入库。
func (s *Storage) loadDayFromDisk(dateKey string) []StoredNotification {
	raw, err := os.ReadFile(dayFilePath(s.dir, dateKey))
	if err == nil {
		entries, skipped := decodeJSONLines(raw)
		if skipped > 0 {
			s.logger.Warn("当天通知文件有 " + strconv.Itoa(skipped) + " 个坏行已跳过: " + dateKey)
		}
		return entries
	}
	if !os.IsNotExist(err) {
		s.logger.Warn("读取当天通知文件失败，按空处理: " + dateKey + ", " + err.Error())
		return nil
	}
	legacyPath := legacyDayFilePath(s.dir, dateKey)
	entries, err := readStoredFile(legacyPath)
	if err != nil {
		s.quarantineDay(dateKey, legacyPath, err)
		return nil
	}
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
	if err := fsutil.EnsureDir(s.dir, fsutil.DirMode); err != nil {
		return err
	}
	if err := fsutil.EnsureDir(s.idIndexDir, fsutil.DirMode); err != nil {
		return err
	}
	_ = os.RemoveAll(s.contentKeyDir)
	return fsutil.EnsureDir(s.contentKeyDir, fsutil.DirMode)
}

// staged 是一天里待落盘的新条目，以及为它们在内存缓存中占位的索引键。
// 索引键先入缓存（这样同一批里的重复能被挡掉），落盘成功后才写进索引文件；
// 落盘失败则整体失效当天缓存，避免索引比数据先一步存在。
type staged struct {
	entries    []StoredNotification
	idKeys     []string
	contentKys []string
}

// Ingest 写入一批通知，去重后返回统计与新插入条目。clientLabel 为来源（缺省 "default"）。
//
// 按日期分组、每天只追加一次：写入量只与本批大小成正比，与当天已有多少条无关。
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
	s.evictColdDays(days)
	return result
}

// evictColdDays 在每批 ingest 结束后逐出「今天和本批之外」日期的内存缓存。
// dayCache 存的是整天的全量通知，不逐出的话长期运行的 daemon 会把历史每一天
// 都常驻内存，最终被 OOM 杀掉。被逐出的日期下次触到时从磁盘重读，
// 补发旧日期通知只是偶发场景，这点 IO 可以接受。
func (s *Storage) evictColdDays(touched map[string]*staged) {
	today := time.Now().In(time.Local).Format("2006-01-02")
	cold := func(dateKey string) bool {
		if dateKey == today {
			return false
		}
		_, ok := touched[dateKey]
		return !ok
	}
	for dateKey := range s.dayCache {
		if cold(dateKey) {
			delete(s.dayCache, dateKey)
		}
	}
	for dateKey := range s.idCache {
		if cold(dateKey) {
			delete(s.idCache, dateKey)
		}
	}
	for dateKey := range s.contentKeyCach {
		if cold(dateKey) {
			delete(s.contentKeyCach, dateKey)
		}
	}
}

// flushDay 把一天的新条目追加写入 .jsonl，成功后才把索引落盘。
// 当天还是旧 .json 数组时，先把「旧数据 + 本批」原子写成 .jsonl 完成迁移，
// 再删掉旧文件——crash 在删除前只会留下一份内容已被 .jsonl 包含的过期副本。
func (s *Storage) flushDay(dateKey string, day *staged) error {
	if len(day.entries) == 0 {
		return nil
	}
	existing := s.loadDay(dateKey)
	err := s.writeDayLines(dateKey, existing, day.entries)
	if err != nil {
		s.invalidateDay(dateKey)
		return err
	}
	s.dayCache[dateKey] = append(existing, day.entries...)

	for _, key := range day.idKeys {
		appendLine(filepath.Join(s.idIndexDir, dateKey+".ids"), key)
	}
	for _, key := range day.contentKys {
		appendLine(filepath.Join(s.contentKeyDir, dateKey+".keys"), key)
	}
	return nil
}

func (s *Storage) writeDayLines(dateKey string, existing, entries []StoredNotification) error {
	newLines, err := encodeJSONLines(entries)
	if err != nil {
		return err
	}
	filePath := dayFilePath(s.dir, dateKey)
	legacyPath := legacyDayFilePath(s.dir, dateKey)

	if !fsutil.Exists(filePath) && fsutil.Exists(legacyPath) {
		existingLines, err := encodeJSONLines(existing)
		if err != nil {
			return err
		}
		if err := fsutil.WriteAtomic(filePath, append(existingLines, newLines...), storedFileMode); err != nil {
			return err
		}
	} else if err := appendDayLines(filePath, newLines); err != nil {
		return err
	}
	// .jsonl 落盘后旧格式文件只是过期副本（含上次迁移 crash 在删除前留下的）。
	if fsutil.Exists(legacyPath) {
		_ = os.Remove(legacyPath)
	}
	return nil
}

// appendDayLines 把整批行一次追加到 .jsonl。文件尾若缺换行（上次 torn write
// 留下的半行），先补一个换行把半行了结成独立的坏行（读取时被跳过），
// 避免新数据接在半行后面一起变成垃圾。
func appendDayLines(path string, lines []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, storedFileMode)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	offset := info.Size()
	if offset > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, offset-1); err != nil {
			f.Close()
			return err
		}
		if last[0] != '\n' {
			lines = append([]byte{'\n'}, lines...)
		}
	}
	if _, err := f.WriteAt(lines, offset); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// invalidateDay 在落盘失败后丢弃当天的内存缓存与索引占位：追加写可能只写进一半，
// 内存状态已不可信。下次触到这一天时从磁盘重建（.ids 只在成功后追加，
// content-key 从数据本身派生），失败批次重发时会与实际落盘的行正确去重。
func (s *Storage) invalidateDay(dateKey string) {
	delete(s.dayCache, dateKey)
	delete(s.idCache, dateKey)
	delete(s.contentKeyCach, dateKey)
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
	pruneByDate(s.dir, cutoff, []string{".jsonl", ".json", ".md"}, func(k string) {
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

// readStoredFile 严格读取旧格式 .json 数组。文件不存在返回空；内容损坏返回错误，
// 交由调用方隔离——静默当成空的一天会让迁移写 .jsonl 时丢掉当天全部旧数据。
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, storedFileMode)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
