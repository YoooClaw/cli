package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/YoooClaw/cli/internal/nanoshell"
)

func (s *server) handleNanoshellGateway(w http.ResponseWriter, r *http.Request, auth authResult) {
	store := nanoshell.Store{Root: s.ctx.Paths.Nanoshell}
	var value any
	var err error
	switch r.URL.Path {
	case "/gateway/nanoshell.apps.list":
		var p struct {
			Limit  *int   `json:"limit"`
			Cursor string `json:"cursor"`
		}
		if !decodeNanoshellBody(w, r, &p) {
			return
		}
		limit := 20
		if p.Limit != nil {
			limit = *p.Limit
		}
		value, err = store.List(auth.scope(), limit, p.Cursor)
	case "/gateway/nanoshell.apps.download":
		var p struct {
			PackageID string `json:"packageId"`
		}
		if !decodeNanoshellBody(w, r, &p) {
			return
		}
		value, err = store.Download(auth.scope(), p.PackageID)
	}
	if err != nil {
		gatewayErr(w, nanoshell.Code(err), err.Error())
		return
	}
	gatewayOK(w, value)
}

// Keep malformed parameters in the RPC business error envelope instead of
// turning them into the dispatcher's generic HTTP_ERROR.
func decodeNanoshellBody(w http.ResponseWriter, r *http.Request, out any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	tooLarge := len(b) > 8192
	b = bytes.TrimSpace(b)
	if err != nil || tooLarge || len(b) == 0 || b[0] != '{' || json.Unmarshal(b, out) != nil {
		gatewayErr(w, "INVALID_PARAMS", "params must be a valid JSON object (max 8192 bytes)")
		return false
	}
	return true
}
