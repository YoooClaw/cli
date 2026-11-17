package daemon

import (
	"github.com/YoooClaw/cli/internal/contextevent"
	"github.com/YoooClaw/cli/internal/recording"
	"net/http"

	imgstore "github.com/YoooClaw/cli/internal/image"
)

func (s *server) handleImageHTTP(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body imgstore.SyncPayload
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := imgstore.Ingest(s.ctx.Paths.Images, body, auth.clientLabel, s.cfg.Image.MaxBytes, s.logger, s.notifyImageContent)
	if err != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", err.Error()))
		return
	}
	writeJSON(w, 200, result)
}

func (s *server) handleImageGateway(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body imgstore.SyncPayload
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := imgstore.Ingest(s.ctx.Paths.Images, body, auth.clientLabel, s.cfg.Image.MaxBytes, s.logger, s.notifyImageContent)
	if err != nil {
		gatewayErr(w, "INVALID_PARAMS", err.Error())
		return
	}
	gatewayOK(w, result)
}

func (s *server) notifyRecordingContent(entry recording.Entry, partial bool) {
	s.publishContext("recordings", "upsert", entry.ClientLabel, 1, partial, "objects", contextevent.Details{Revision: entry.ContextRevision}, entry.ID)
}
func (s *server) notifyImageContent(entry imgstore.Entry) {
	s.publishContext("images", "upsert", entry.ClientLabel, 1, false, "objects", contextevent.Details{Revision: contextRevision(entry)}, entry.ImageID)
}
