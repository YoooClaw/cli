package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/YoooClaw/cli/internal/healthdata"
	"github.com/YoooClaw/cli/internal/relay"
)

// handleHealthDataIngest 接收精简日快照，并兼容归档旧客户端原始批次。
func (s *server) handleHealthDataIngest(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	batch, err := healthdata.Prepare(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", err.Error()))
		return
	}
	if collectedAtTooFarInFuture(batch.CollectedAt()) {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", "collectedAt is too far in the future"))
		return
	}
	if err := s.healthDataStorage.Append(batch); err != nil {
		s.logger.Error(fmt.Sprintf("[health-data] requestId=%s write failed: %v", r.Header.Get(relay.InternalRequestIDHeader), err))
		writeJSON(w, http.StatusInternalServerError, errBody("HEALTH_DATA_WRITE_FAILED", "健康数据保存失败"))
		return
	}
	s.logger.Info(fmt.Sprintf("[health-data] requestId=%s ingested=%d deleted=%d",
		r.Header.Get(relay.InternalRequestIDHeader), batch.ItemCount, batch.DeletedCount))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ingested": batch.ItemCount, "deleted": batch.DeletedCount})
}
