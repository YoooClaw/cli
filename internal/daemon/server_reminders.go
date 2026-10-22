package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/reminders"
)

func (s *server) handleReminderIngest(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, reminders.UploadLimit))
	if err != nil {
		writeJSON(w, 413, errBody("PAYLOAD_TOO_LARGE", "完整提醒事项快照超过大小限制，未更新"))
		return
	}
	body, err := reminders.Decode(raw)
	if err != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", err.Error()))
		return
	}
	collectedAt, _ := time.Parse(time.RFC3339Nano, body.CollectedAt)
	if collectedAtTooFarInFuture(collectedAt) {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "collectedAt is too far in the future"))
		return
	}
	result, err := s.reminderStorage.Replace(body)
	if err != nil {
		s.logger.Error(fmt.Sprintf("[reminders] snapshot write failed: %v", err))
		writeJSON(w, 500, errBody("REMINDERS_WRITE_FAILED", "提醒事项保存失败"))
		return
	}
	s.logger.Info(fmt.Sprintf("[reminders] ingested=%d applied=%t bytes=%d", result.Ingested, result.Applied, len(raw)))
	writeJSON(w, 200, result)
}

func (s *server) handleReminderAssets(w http.ResponseWriter, r *http.Request) {
	id, _ := assets.ReminderRoute(r.URL.Path)
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAssetError(w, &assets.Error{Status: 400, Code: "INVALID_PARAMS", Action: "none"})
		return
	}
	response, e := s.assetsReader().ReadReminders(r.Context(), s.reminderStorage, id, params)
	if e != nil {
		writeAssetError(w, e)
		return
	}
	raw, err := json.Marshal(response)
	if err != nil || len(raw) > assets.ResponseLimit {
		writeAssetError(w, &assets.Error{Status: 500, Code: "SOURCE_READ_FAILED", Action: "retry", Retryable: true})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	s.logger.Info(fmt.Sprintf("assets.reminders status=200 detail=%t items=%d bytes=%d hasNext=%t", id != "", len(response.Items), len(raw), response.NextCursor != nil))
}
