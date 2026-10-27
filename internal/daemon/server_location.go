package daemon

import (
	"fmt"
	"net/http"

	"github.com/YoooClaw/cli/internal/location"
	"github.com/YoooClaw/cli/internal/relay"
)

// handleLocationIngest 接收 Relay 或宿主转发的位置记录，鉴权沿用现有入口。
func (s *server) handleLocationIngest(w http.ResponseWriter, r *http.Request) {
	var body location.Payload
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", "items must contain at least one location"))
		return
	}
	for i, item := range body.Items {
		if err := item.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", fmt.Sprintf("items[%d]: %s", i, err)))
			return
		}
	}
	if err := s.locationStorage.Append(body.Items); err != nil {
		s.logger.Error(fmt.Sprintf("[locations] requestId=%s write failed: %v", r.Header.Get(relay.InternalRequestIDHeader), err))
		writeJSON(w, http.StatusInternalServerError, errBody("LOCATION_WRITE_FAILED", "位置数据保存失败"))
		return
	}
	s.logger.Info(fmt.Sprintf("[locations] requestId=%s ingested=%d", r.Header.Get(relay.InternalRequestIDHeader), len(body.Items)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ingested": len(body.Items)})
}
