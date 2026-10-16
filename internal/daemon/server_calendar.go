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
	if err := s.calendarStorage.Replace(batch); err != nil {
		s.logger.Error(fmt.Sprintf("[calendars] requestId=%s write failed: %v", r.Header.Get(relay.InternalRequestIDHeader), err))
		writeJSON(w, http.StatusInternalServerError, errBody("CALENDAR_WRITE_FAILED", "日程数据保存失败"))
		return
	}
	s.logger.Info(fmt.Sprintf("[calendars] requestId=%s range=%s..%s ingested=%d files=%d",
		r.Header.Get(relay.InternalRequestIDHeader), body.StartDate, body.EndDate, batch.ItemCount(), batch.DayCount()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ingested": batch.ItemCount(), "filesWritten": batch.DayCount()})
}
