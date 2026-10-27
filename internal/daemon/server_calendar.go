package daemon

import (
	"fmt"
	"net/http"

	"github.com/YoooClaw/cli/internal/calendar"
	"github.com/YoooClaw/cli/internal/relay"
)

// handleCalendarIngest 复用现有 HTTP/Relay 鉴权，接收一轮完整日程列表。
func (s *server) handleCalendarIngest(w http.ResponseWriter, r *http.Request) {
	var body calendar.Payload
	if !decodeBody(w, r, &body) {
		return
	}
	batch, err := calendar.Prepare(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", err.Error()))
		return
	}
	if collectedAtTooFarInFuture(batch.CollectedAt()) {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", "collectedAt is too far in the future"))
		return
	}
	result, err := s.calendarStorage.Replace(batch)
	if err != nil {
		s.logger.Error(fmt.Sprintf("[calendars] requestId=%s write failed: %v", r.Header.Get(relay.InternalRequestIDHeader), err))
		writeJSON(w, http.StatusInternalServerError, errBody("CALENDAR_WRITE_FAILED", "日程数据保存失败"))
		return
	}
	requestID := r.Header.Get(relay.InternalRequestIDHeader)
	if !result.Applied {
		s.logger.Warn(fmt.Sprintf("[calendars] requestId=%s skipped stale snapshot collectedAt=%s", requestID, body.CollectedAt))
	}
	ingested := 0
	if result.Applied {
		ingested = batch.ItemCount()
	}
	s.logger.Info(fmt.Sprintf("[calendars] requestId=%s range=%s..%s ingested=%d files=%d applied=%t",
		requestID, body.StartDate, body.EndDate, ingested, result.FilesWritten, result.Applied))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": result.Applied, "ingested": ingested, "filesWritten": result.FilesWritten})
}
