package notif

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var dateDirRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

const (
	idIndexDirName         = ".ids"
	contentKeyIndexDirName = ".keys"
	unitSep                = "\x1f"
)

// Logger 是存储层依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
}

// IngestResult 是一次 ingest 的统计 + 新插入条目。
type IngestResult struct {
	Received         int                  `json:"received"`
	Ingested         int                  `json:"ingested"`
	DedupedByID      int                  `json:"dedupedById"`
	DedupedByContent int                  `json:"dedupedByContent"`
	Invalid          int                  `json:"invalid"`
	Inserted         []StoredNotification `json:"-"`
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
	}
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

// Ingest 写入一批通知，去重后返回统计与新插入条目。clientLabel 为来源（缺省 "default"）。
func (s *Storage) Ingest(items []RawNotification, clientLabel string) IngestResult {
	if clientLabel == "" {
		clientLabel = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result := IngestResult{Received: len(items)}
	for _, n := range items {
		kind, entry := s.writeOne(n, clientLabel)
		switch kind {
		case "ingested":
			result.Ingested++
			result.Inserted = append(result.Inserted, entry)
		case "dedupedById":
			result.DedupedByID++
		case "dedupedByContent":
			result.DedupedByContent++
		case "invalid":
			result.Invalid++
		}
	}
	s.prune()
	return result
}

func (s *Storage) writeOne(n RawNotification, clientLabel string) (string, StoredNotification) {
	ts, ok := ParseTime(n.Timestamp)
	if !ok {
		s.logger.Warn("忽略非法 timestamp 的通知: " + n.ID)
		return "invalid", StoredNotification{}
	}
	dateKey := ts.In(time.Local).Format("2006-01-02")
	filePath := filepath.Join(s.dir, dateKey+".json")
	normalizedID := strings.TrimSpace(n.ID)
	entry := s.buildStored(n, clientLabel)

	if normalizedID != "" && s.hasID(dateKey, labelOrLegacy(entry.ClientLabel), normalizedID) {
		return "dedupedById", StoredNotification{}
	}
	if s.hasContentKey(dateKey, filePath, entry) {
		return "dedupedByContent", StoredNotification{}
	}

	if entry.AppDisplayName == "" {
		entry.AppDisplayName = entry.AppName
	}
	arr := readStoredFile(filePath)
	arr = append(arr, entry)
	if data, err := json.MarshalIndent(arr, "", "  "); err == nil {
		_ = os.WriteFile(filePath, data, 0o644)
	}

	if normalizedID != "" {
		s.recordID(dateKey, labelOrLegacy(entry.ClientLabel), normalizedID)
	}
	s.recordContentKey(dateKey, filePath, entry)
	return "ingested", entry
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

func (s *Storage) recordID(dateKey, label, id string) {
	set := s.idSet(dateKey)
	key := s.idKey(label, id)
	if set[key] {
		return
	}
	appendLine(filepath.Join(s.idIndexDir, dateKey+".ids"), key)
	set[key] = true
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

func (s *Storage) contentKeySet(dateKey, filePath string) map[string]bool {
	if set, ok := s.contentKeyCach[dateKey]; ok {
		return set
	}
	set := map[string]bool{}
	keyPath := filepath.Join(s.contentKeyDir, dateKey+".keys")
	for _, item := range readStoredFile(filePath) {
		set[contentKey(item)] = true
	}
	if len(set) > 0 {
		var b strings.Builder
		for k := range set {
			b.WriteString(k)
			b.WriteString("\n")
		}
		_ = os.WriteFile(keyPath, []byte(b.String()), 0o644)
	} else {
		_ = os.Remove(keyPath)
	}
	s.contentKeyCach[dateKey] = set
	return set
}

func (s *Storage) hasContentKey(dateKey, filePath string, e StoredNotification) bool {
	set := s.contentKeySet(dateKey, filePath)
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

func (s *Storage) recordContentKey(dateKey, filePath string, e StoredNotification) {
	set := s.contentKeySet(dateKey, filePath)
	key := contentKey(e)
	if set[key] {
		return
	}
	appendLine(filepath.Join(s.contentKeyDir, dateKey+".keys"), key)
	set[key] = true
}

func (s *Storage) prune() {
	if s.cfg.RetentionDays == nil {
		return
	}
	cutoff := time.Now().Add(-time.Duration(*s.cfg.RetentionDays) * 24 * time.Hour).In(time.Local).Format("2006-01-02")
	pruneByDate(s.dir, cutoff, []string{".json", ".md"}, func(k string) { delete(s.idCache, k); delete(s.contentKeyCach, k) }, false)
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

func readStoredFile(filePath string) []StoredNotification {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	var items []StoredNotification
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	return items
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
