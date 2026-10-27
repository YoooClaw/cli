package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/webpage"
)

func assetKind(path string) string {
	if _, ok := assets.ReminderRoute(path); ok {
		return "reminders"
	}
	if _, _, ok := assets.SleepRoute(path); ok {
		return "health-data-sleep"
	}
	if !strings.HasPrefix(path, "/assets/") {
		return ""
	}
	kind := strings.TrimPrefix(path, "/assets/")
	if !assets.Supported(kind) {
		return ""
	}
	return kind
}

// Every profile belongs to one owner; reads include all sources in that profile.
func (s *server) capabilities() []string {
	out := append([]string(nil), Capabilities...)
	out = append(out, "assets.notifications.read.v1", "assets.web-pages.read.v1",
		"assets.reminders.read.v1", "assets.locations.read.v1", "assets.calendars.read.v1", "assets.health-data.read.v1", "assets.health-data.sleep.read.v1")
	return out
}

func (s *server) assetsReader() *assets.Service {
	s.assetOnce.Do(func() {
		p := s.ctx.Paths
		sources := map[string]assets.Source{
			"notifications": {Dir: p.Notifications, WithReadView: s.storage.WithReadView},
			"locations":     {Dir: p.Locations, WithReadView: s.locationStorage.WithReadView},
			"calendars":     {Dir: p.Calendars, WithReadView: s.calendarStorage.WithReadView},
			"health-data":   {Dir: p.HealthData, WithReadView: s.healthDataStorage.WithReadView},
			"web-pages":     {Dir: p.WebPages, WithReadView: webpage.WithReadView},
		}
		s.assetReader = assets.New(s.ctx.Profile, sources)
	})
	return s.assetReader
}

func writeAssetError(w http.ResponseWriter, e *assets.Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(struct {
		OK    bool          `json:"ok"`
		Error *assets.Error `json:"error"`
	}{Error: e})
}

// messageBridge only forwards POST. Accept its bounded GET envelope without
// exposing a write operation or changing the public GET/query/cursor contract.
func (s *server) handleAssetRequest(w http.ResponseWriter, r *http.Request, kind string) {
	if r.Method == http.MethodPost {
		var envelope struct {
			Method string          `json:"method"`
			Body   json.RawMessage `json:"body"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil || envelope.Method != http.MethodGet ||
			(len(envelope.Body) != 0 && string(envelope.Body) != "null") || decoder.Decode(new(any)) != io.EOF {
			writeAssetError(w, &assets.Error{Status: 400, Code: "INVALID_PARAMS", Message: "Expected a read-only GET envelope", Action: "none"})
			return
		}
	} else if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeAssetError(w, &assets.Error{Status: 405, Code: "METHOD_NOT_ALLOWED", Message: "Only asset reads are supported", Action: "none"})
		return
	}
	if kind == "reminders" {
		s.handleReminderAssets(w, r)
		return
	}
	if kind == "health-data-sleep" {
		s.handleSleepAssets(w, r)
		return
	}
	s.handleAssets(w, r, kind)
}

func (s *server) handleAssets(w http.ResponseWriter, r *http.Request, kind string) {
	start := time.Now()
	params, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		writeAssetError(w, &assets.Error{Status: 400, Code: "INVALID_PARAMS", Message: "Invalid query parameters", Action: "none"})
		return
	}
	response, err := s.assetsReader().Read(r.Context(), kind, params)
	if err != nil {
		writeAssetError(w, err)
		s.logger.Info(fmt.Sprintf("assets.read kind=%s status=%d code=%s elapsedMs=%d", kind, err.Status, err.Code, time.Since(start).Milliseconds()))
		return
	}
	// Marshal once and write without Encoder's extra newline: the hard limit
	// applies to exactly these UTF-8 bytes, including cursor and warnings.
	raw, e := json.Marshal(response)
	if e != nil || len(raw) > assets.ResponseLimit {
		writeAssetError(w, &assets.Error{Status: 500, Code: "SOURCE_READ_FAILED", Message: "Unable to encode assets", Retryable: true, Action: "retry"})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	s.logger.Info(fmt.Sprintf("assets.read kind=%s status=200 items=%d bytes=%d hasNext=%t elapsedMs=%d", kind, len(response.Items), len(raw), response.NextCursor != nil, time.Since(start).Milliseconds()))
}

func (s *server) handleSleepAssets(w http.ResponseWriter, r *http.Request) {
	date, id, _ := assets.SleepRoute(r.URL.Path)
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAssetError(w, &assets.Error{Status: 400, Code: "INVALID_PARAMS", Action: "none"})
		return
	}
	response, e := s.assetsReader().ReadSleep(r.Context(), date, id, params)
	if e != nil {
		writeAssetError(w, e)
		s.logger.Info(fmt.Sprintf("assets.sleep status=%d code=%s", e.Status, e.Code))
		return
	}
	raw, err := json.Marshal(response)
	if err != nil || len(raw) > assets.ResponseLimit {
		writeAssetError(w, &assets.Error{Status: 500, Code: "SOURCE_READ_FAILED", Action: "retry", Retryable: true})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	s.logger.Info(fmt.Sprintf("assets.sleep status=200 items=%d bytes=%d hasNext=%t", len(response.Items), len(raw), response.NextCursor != nil))
}
