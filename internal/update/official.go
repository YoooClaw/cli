package update

import (
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// BeginOfficial reports an explicit official installer invocation without pretending
// that its version was authorized by the rollout service.
func BeginOfficial(id Identity, target string) (Attempt, error) {
	if _, err := Compare(target, target); err != nil {
		return Attempt{}, err
	}
	a := Attempt{Identity: id, UpdateID: ID(), Trigger: "cli", Artifact: Artifact{TargetVersion: target}, CreatedAt: time.Now().UTC(), Official: true}
	if err := os.MkdirAll(WorkDir(a), 0700); err != nil {
		return a, err
	}
	if err := fsutil.WriteJSON(JobPath(a), a, 0600); err != nil {
		return a, err
	}
	Record(a, "gate", "succeeded", nil)
	return a, nil
}
func OfficialEvent(a Attempt, stage, result string) error {
	if !a.Official {
		return fail("INVALID_ATTEMPT", "不是官方安装器上报上下文")
	}
	if _, err := a.Identity.Key(); err != nil {
		return err
	}
	allowed := map[string]bool{"download": true, "verify": true, "apply": true, "restart": true, "activate": true}
	if !allowed[stage] || (result != "started" && result != "succeeded" && result != "failed" && result != "skipped") {
		return fail("INVALID_EVENT", "无效的安装事件")
	}
	var eventErr error
	if result == "failed" {
		eventErr = fail("INSTALLER_FAILED", "官方安装器在 "+stage+" 阶段失败，请查看本地安装输出")
	}
	Record(a, stage, result, eventErr)
	if stage == "apply" && result == "succeeded" {
		var installation Installation
		ok, err := fsutil.ReadJSON(markerPath(a.Identity.Root), &installation)
		if err != nil || !ok {
			return fail("INSTALLATION_NOT_OWNED", "安装器未登记目标位置")
		}
		exe, err := filepath.EvalSymlinks(installation.Executable)
		if err != nil {
			return err
		}
		a.Target = exe
		a.Ready = true
		if err := fsutil.WriteJSON(JobPath(a), a, 0600); err != nil {
			return err
		}
		e := EventFor(a, "activate", "started", nil)
		e.InstalledVersion = a.Artifact.TargetVersion
		_ = recordEvent(a.Identity, e)
	}
	return nil
}
