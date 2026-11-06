package daemon

import (
	"net/http"

	imgstore "github.com/YoooClaw/cli/internal/image"
)

func (s *server) handleImageHTTP(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body imgstore.SyncPayload
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := imgstore.Ingest(s.ctx.Paths.Images, body, auth.clientLabel, s.cfg.Image.MaxBytes, s.logger)
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
	result, err := imgstore.Ingest(s.ctx.Paths.Images, body, auth.clientLabel, s.cfg.Image.MaxBytes, s.logger)
	if err != nil {
		gatewayErr(w, "INVALID_PARAMS", err.Error())
		return
	}
	gatewayOK(w, result)
}
