package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/YoooClaw/cli/internal/filelockdiag"
)

// logRecordingWriteFailure 先同步记录请求上下文，再异步查询 Windows 文件占用者。
// Restart Manager 诊断不能拖慢 Relay 的失败响应；每次写盘失败都独立采集现场。
func (s *server) logRecordingWriteFailure(recordingID, relayID string, err error) {
	fields := "recordingId=" + recordingID
	if relayID = strings.TrimSpace(relayID); relayID != "" {
		fields += ", relay=" + relayID
	}
	s.logger.Error("[recording-result] 写入失败: " + fields + ", error=" + err.Error())

	target, ok := recordingIndexRenameTarget(err)
	if !ok {
		return
	}
	go s.logFileLockHolders(target, recordingID, relayID)
}

func recordingIndexRenameTarget(err error) (string, bool) {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !strings.EqualFold(linkErr.Op, "rename") {
		return "", false
	}
	target := strings.TrimSpace(linkErr.New)
	if target == "" || !strings.EqualFold(filepath.Base(target), "index.json") {
		return "", false
	}
	return filepath.Clean(target), true
}

func (s *server) logFileLockHolders(path, recordingID, relayID string) {
	holders, supported, err := filelockdiag.Lookup(path)
	if !supported {
		return
	}
	context := "recordingId=" + recordingID
	if relayID != "" {
		context += ", relay=" + relayID
	}
	if err != nil {
		s.logger.Warn("[recording-store] 查询 index.json 占用进程失败: " + context +
			", path=" + path + ", error=" + err.Error())
		return
	}
	if len(holders) == 0 {
		s.logger.Warn("[recording-store] index.json 替换被拒绝，但 Restart Manager 未识别到占用进程: " +
			context + ", path=" + path + "（可能是瞬时占用、ACL、只读属性或安全软件）")
		return
	}
	parts := make([]string, 0, len(holders))
	for _, holder := range holders {
		parts = append(parts, fmt.Sprintf("{pid=%d, app=%q, service=%q, type=%d, restartable=%t}",
			holder.PID, cleanLogField(holder.Application), cleanLogField(holder.Service),
			holder.ApplicationType, holder.Restartable))
	}
	// Restart Manager 能确认哪些进程正在使用该文件，但不暴露每个句柄的
	// FILE_SHARE_DELETE 标志；因此只称“相关占用进程”，不武断归因其中某一个。
	s.logger.Error("[recording-store] index.json 相关占用进程（Restart Manager）: " + context +
		", path=" + path + ", holders=[" + strings.Join(parts, ", ") + "]")
}

func cleanLogField(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
