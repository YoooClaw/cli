package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
)

func (s *server) handleAssetSummary(w http.ResponseWriter, r *http.Request, auth authResult) {
	start := time.Now()
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAssetError(w, &assets.Error{Status: 400, Code: "INVALID_PARAMS", Message: "Invalid query parameters", Action: "none"})
		return
	}
	response, e := s.assetsReader().ReadSummary(r.Context(), params, assets.SummaryStores{
		Notifications: s.storage, Reminders: s.reminderStorage, Recordings: s.recordingStorage, RecordingScope: auth.scope(),
	})
	if e != nil {
		writeAssetError(w, e)
		s.logger.Info(fmt.Sprintf("assets.summary status=%d code=%s elapsedMs=%d", e.Status, e.Code, time.Since(start).Milliseconds()))
		return
	}
	raw, err := json.Marshal(response)
	if err != nil || len(raw) > assets.ResponseLimit {
		writeAssetError(w, &assets.Error{Status: 500, Code: "SOURCE_READ_FAILED", Message: "Unable to encode asset summary", Retryable: true, Action: "retry"})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	s.logger.Info(fmt.Sprintf("assets.summary status=200 date=%s categories=%d partial=%t bytes=%d elapsedMs=%d", response.Date, len(response.Assets), response.Partial, len(raw), time.Since(start).Milliseconds()))
}
