package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/image"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/webpage"
)

// stagingTTL 是没走到导入的 staging 副本的保留时长；有 report 的（PARTIAL/失败）不受此限制。
const stagingTTL = 7 * 24 * time.Hour

// notificationBatchSize 是通知单次落盘的上限（同一天内分批）。
const notificationBatchSize = 500

// Service 在 daemon 进程内执行预览与导入。
//
// 导入必须经过 daemon：通知与录音存储把当天数据、索引缓存在内存里，绕过它
// 直接写文件会被 daemon 的下一次落盘覆盖。这与插件要求「经运行中的目标插件
// 导入」是同一个约束。
type Service struct {
	// Dir 是 staging 目录（<profile>/transfers）。
	Dir           string
	Roots         Roots
	Notifications *notif.Storage
	Recordings    *recording.Storage
	mu            sync.Mutex
}

// Request 是 daemon /transfer 的请求体（字段与插件本地端点一致）。
type Request struct {
	Action          string `json:"action"`
	File            string `json:"file,omitempty"`
	LocalTransferID string `json:"localTransferId,omitempty"`
	PlanID          string `json:"planId,omitempty"`
	Resume          bool   `json:"resume,omitempty"`
}

// Decision 是预览里单条记录的计划动作。
type Decision struct {
	Type     string `json:"type"`
	RecordID string `json:"recordId"`
	Action   string `json:"action"`
}

// Result 是导入里单条记录的实际结果。
type Result struct {
	Type     string `json:"type"`
	RecordID string `json:"recordId"`
	Outcome  string `json:"outcome"`
	Error    string `json:"error,omitempty"`
}

type plan struct {
	Decisions                []Decision     `json:"decisions"`
	TargetVersion            string         `json:"targetVersion"`
	Warnings                 []string       `json:"warnings"`
	TargetWarnings           []string       `json:"targetWarnings"`
	NotificationMemoryPolicy string         `json:"notificationMemoryPolicy"`
	Capabilities             map[string]any `json:"capabilities"`
}

// Handle 串行执行一个请求（同一时刻只跑一个迁移动作）。
func (s *Service) Handle(req Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Action {
	case "capabilities":
		return Capabilities(), nil
	case "preview":
		if req.File == "" {
			return nil, fail("PACKAGE_REQUIRED")
		}
		return s.stage(req.File)
	case "import":
		return s.importPlan(req.LocalTransferID, req.PlanID, req.Resume)
	}
	return nil, fail("UNKNOWN_ACTION")
}

func (s *Service) preview(m *Manifest) (*plan, error) {
	targetWarnings := []string{}
	lookups := map[string]map[string]map[string]any{}
	for _, typ := range Types {
		lookup := map[string]map[string]any{}
		lookups[typ] = lookup
		root := s.Roots[typ]
		if root == "" || !dirExists(root) {
			continue
		}
		if typ != TypeNotifications && !pathExists(filepath.Join(root, "index.json")) {
			continue
		}
		list, err := entries(root, typ, func(w string) { targetWarnings = append(targetWarnings, "target "+w) })
		if err != nil {
			return nil, err
		}
		for _, item := range list {
			lookup[matchKey(typ, item)] = item
		}
	}
	type planned struct {
		Type     string `json:"type"`
		RecordID string `json:"recordId"`
		Action   string `json:"action"`
		// Current 只进 targetVersion，不回传给调用方。
		Current *string `json:"current"`
	}
	items := make([]planned, 0, len(m.Records))
	decisions := make([]Decision, 0, len(m.Records))
	for _, r := range m.Records {
		root := s.Roots[r.Type]
		if root == "" {
			return nil, fail("STORAGE_UNAVAILABLE")
		}
		current := lookups[r.Type][matchKey(r.Type, r.Data)]
		action, err := compare(r, current, root)
		if err != nil {
			action = OutcomeConflict
		}
		var cur *string
		if current != nil {
			n, err := normalized(r.Type, current)
			if err != nil {
				n = portable(r.Type, current)
			}
			c := canonical(n)
			cur = &c
		}
		items = append(items, planned{Type: r.Type, RecordID: r.RecordID, Action: action, Current: cur})
		decisions = append(decisions, Decision{Type: r.Type, RecordID: r.RecordID, Action: action})
	}
	// targetVersion 只覆盖计划涉及的记录的当前状态：通知 ingress 是持续的，
	// 用全量目标数据做指纹的话，preview 与 import 之间任何一条无关新通知都会
	// 让计划失效。收窄之后仍能挡住「目标那条记录变了」的真实漂移。
	return &plan{
		Decisions: decisions, TargetVersion: sha(canonical(items)), Warnings: m.Warnings,
		TargetWarnings: targetWarnings, NotificationMemoryPolicy: notif.MemoryPolicySkipHistory,
		Capabilities: Capabilities(),
	}, nil
}

// pruneStaging 清理从未进入导入的旧 staging 副本；留下 report.json 的一律保留。
func (s *Service) pruneStaging() {
	list, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range list {
		if !uuidRE.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(s.Dir, e.Name())
		if pathExists(filepath.Join(path, "report.json")) {
			continue
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) >= stagingTTL {
			_ = os.RemoveAll(path)
		}
	}
}

// stage 校验包、只复制清单列出的资源到私有 staging 目录，再生成预览计划。
// --file 永远只到预览为止，合并必须走 --local + --plan。
func (s *Service) stage(file string) (any, error) {
	if err := fsutil.EnsureDir(s.Dir, fsutil.DirMode); err != nil {
		return nil, err
	}
	s.pruneStaging()
	source, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	m, err := ReadPackage(source, ReadOptions{})
	if err != nil {
		return nil, err
	}
	localID := newUUID()
	local := filepath.Join(s.Dir, localID)
	if err := os.MkdirAll(filepath.Join(local, "blobs"), 0o700); err != nil {
		return nil, err
	}
	cleanup := func(err error) (any, error) {
		_ = os.RemoveAll(local)
		return nil, err
	}
	// 不递归复制包内任意内容，只复制校验过的清单与资源。
	mf, err := safeFile(source, "manifest.json")
	if err != nil {
		return cleanup(err)
	}
	if err := copyFile(mf, filepath.Join(local, "manifest.json")); err != nil {
		return cleanup(err)
	}
	for _, r := range m.Records {
		for _, a := range r.Assets {
			dest := filepath.Join(local, "blobs", a.Hash)
			if pathExists(dest) {
				continue
			}
			src, err := safeFile(source, "blobs/"+a.Hash)
			if err != nil {
				return cleanup(err)
			}
			if err := copyFile(src, dest); err != nil {
				return cleanup(err)
			}
		}
	}
	verified, err := ReadPackage(local, ReadOptions{})
	if err != nil {
		return cleanup(err)
	}
	p, err := s.preview(verified)
	if err != nil {
		return cleanup(err)
	}
	planID := sha(canonical(map[string]any{
		"package": toAny(verified), "decisions": toAny(p.Decisions), "targetVersion": p.TargetVersion,
		"warnings": toAny(p.Warnings), "targetWarnings": toAny(p.TargetWarnings),
		"notificationMemoryPolicy": p.NotificationMemoryPolicy, "capabilities": toAny(p.Capabilities),
	}))
	stored := map[string]any{"planId": planID, "plan": p}
	if err := writePrivateJSON(filepath.Join(local, "plan.json"), stored); err != nil {
		return cleanup(err)
	}
	return map[string]any{
		"localTransferId": localID, "planId": planID, "decisions": p.Decisions,
		"targetVersion": p.TargetVersion, "warnings": p.Warnings, "targetWarnings": p.TargetWarnings,
		"notificationMemoryPolicy": p.NotificationMemoryPolicy, "capabilities": p.Capabilities,
		"nextCommand": "yoooclaw transfer import --local " + localID + " --plan " + planID,
	}, nil
}

func (s *Service) importPlan(localID, planID string, resume bool) (any, error) {
	if !uuidRE.MatchString(localID) {
		return nil, fail("INVALID_LOCAL_ID")
	}
	local := filepath.Join(s.Dir, localID)
	if !dirExists(local) {
		return nil, fail("LOCAL_TRANSFER_NOT_FOUND")
	}
	// staging 是本机 0700 目录下、preview 时已逐字节校验过的副本，这里只做结构与体积校验。
	m, err := ReadPackage(local, ReadOptions{SkipBlobHash: true})
	if err != nil {
		return nil, err
	}
	var stored struct {
		PlanID string `json:"planId"`
		Plan   plan   `json:"plan"`
	}
	raw, err := os.ReadFile(filepath.Join(local, "plan.json"))
	if err != nil || json.Unmarshal(raw, &stored) != nil {
		return nil, fail("PLAN_REQUIRED")
	}
	if planID != stored.PlanID {
		return nil, fail("PLAN_REQUIRED")
	}
	reportPath := filepath.Join(local, "report.json")
	hasPrevious := pathExists(reportPath)
	fresh, err := s.preview(m)
	if err != nil {
		return nil, err
	}
	if !resume && fresh.TargetVersion != stored.Plan.TargetVersion {
		return nil, fail("PLAN_STALE")
	}
	if resume && !hasPrevious {
		return nil, fail("NOTHING_TO_RESUME")
	}
	results := []Result{}
	progress := func() error {
		return writePrivateJSON(reportPath, map[string]any{"state": "IMPORTING", "planId": stored.PlanID, "localTransferId": localID, "results": results})
	}
	if err := progress(); err != nil {
		return nil, err
	}
	var units [][]Record
	days := map[string][]Record{}
	var dayOrder []string
	for _, r := range m.Records {
		if r.Type != TypeNotifications {
			units = append(units, []Record{r})
			continue
		}
		// 分日必须问存储层要，别再实现一份日期格式化。
		day, ok := notif.DateKeyOf(stringField(r.Data, "timestamp"))
		if !ok {
			day = ""
		}
		if _, seen := days[day]; !seen {
			dayOrder = append(dayOrder, day)
		}
		days[day] = append(days[day], r)
	}
	for _, day := range dayOrder {
		records := days[day]
		for offset := 0; offset < len(records); offset += notificationBatchSize {
			end := min(offset+notificationBatchSize, len(records))
			units = append(units, records[offset:end])
		}
	}
	for _, unit := range units {
		var outcomes []string
		var err error
		if unit[0].Type == TypeNotifications {
			outcomes, err = s.importNotifications(unit)
		} else {
			var outcome string
			outcome, err = s.importOne(unit[0], local)
			outcomes = []string{outcome}
		}
		for i, r := range unit {
			if err != nil {
				results = append(results, Result{Type: r.Type, RecordID: r.RecordID, Outcome: OutcomeFailed, Error: err.Error()})
			} else {
				results = append(results, Result{Type: r.Type, RecordID: r.RecordID, Outcome: outcomes[i]})
			}
		}
		_ = progress()
	}
	// 只按实际写入结果判定。源端导出的 warning 会一直跟着 manifest，
	// 不能让同一个包每次导入都报 PARTIAL。
	state := "SUCCEEDED"
	for _, r := range results {
		if r.Outcome == OutcomeFailed || r.Outcome == OutcomeConflict {
			state = "PARTIAL"
			break
		}
	}
	result := map[string]any{
		"state": state, "planId": stored.PlanID, "localTransferId": localID, "results": results,
		"warnings": m.Warnings, "targetWarnings": fresh.TargetWarnings, "reportPath": reportPath,
	}
	if err := writePrivateJSON(reportPath, result); err != nil {
		return nil, err
	}
	// 成功即清理 staging 副本；PARTIAL/失败保留可重试包与 report，等用户处理。
	if state == "SUCCEEDED" {
		_ = os.RemoveAll(local)
		delete(result, "reportPath")
		result["staging"] = "cleaned"
	}
	return result, nil
}

func (s *Service) importNotifications(records []Record) ([]string, error) {
	if s.Notifications == nil {
		return nil, fail("STORAGE_UNAVAILABLE")
	}
	dateKey, ok := notif.DateKeyOf(stringField(records[0].Data, "timestamp"))
	if !ok {
		return nil, fail("INVALID_NOTIFICATION")
	}
	for _, r := range records {
		if day, _ := notif.DateKeyOf(stringField(r.Data, "timestamp")); day != dateKey {
			return nil, fail("MIXED_DATE_BATCH")
		}
	}
	root := s.Roots[TypeNotifications]
	outcomes := make([]string, 0, len(records))
	err := s.Notifications.ImportDay(dateKey, func(existing []notif.StoredNotification, admit func(notif.StoredNotification) bool) error {
		lookup := make(map[string]map[string]any, len(existing))
		for _, item := range existing {
			m := toMap(item)
			lookup[matchKey(TypeNotifications, m)] = m
		}
		for _, r := range records {
			key := matchKey(r.Type, r.Data)
			outcome, err := compare(r, lookup[key], root)
			if err != nil {
				return err
			}
			if outcome == OutcomeAdded {
				entry, err := fromMap[notif.StoredNotification](r.Data)
				if err != nil {
					return err
				}
				entry.Transfer = &notif.TransferMark{Origin: r.Origin, RecordID: r.RecordID, MemoryPolicy: notif.MemoryPolicySkipHistory}
				if admit(*entry) {
					lookup[key] = toMap(*entry)
				} else {
					outcome = OutcomeSkipped
				}
			}
			outcomes = append(outcomes, outcome)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcomes, nil
}

func (s *Service) importOne(r Record, packageRoot string) (string, error) {
	root := s.Roots[r.Type]
	if root == "" {
		return "", fail("STORAGE_UNAVAILABLE")
	}
	if err := fsutil.EnsureDir(root, fsutil.DirMode); err != nil {
		return "", err
	}
	outcome := ""
	var err error
	switch r.Type {
	case TypeRecordings:
		if s.Recordings == nil {
			return "", fail("STORAGE_UNAVAILABLE")
		}
		err = s.Recordings.ImportEntry(r.RecordID, func(cur *recording.Entry) (*recording.Entry, error) {
			return mergeEntry(r, root, packageRoot, cur, &outcome)
		})
	case TypeImages:
		err = image.ImportEntry(root, r.RecordID, func(cur *image.Entry) (*image.Entry, error) {
			return mergeEntry(r, root, packageRoot, cur, &outcome)
		})
	case TypeWebPages:
		err = webpage.ImportEntry(root, r.RecordID, func(cur *webpage.Entry) (*webpage.Entry, error) {
			return mergeEntry(r, root, packageRoot, cur, &outcome)
		})
	default:
		err = fail("INVALID_SCOPE")
	}
	if err != nil {
		return "", err
	}
	return outcome, nil
}

func writePrivateJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, raw, fsutil.SecretFileMode)
}
