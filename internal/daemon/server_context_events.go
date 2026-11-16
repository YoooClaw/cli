package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/clientlabel"
	"github.com/YoooClaw/cli/internal/contextevent"
)

func contextEventPath(path string) bool {
	return path == "/context/events" || path == "/context/events/stream"
}
func (s *server) contextJournal() (*contextevent.Log, error) {
	s.contextOnce.Do(func() {
		s.contextDelivery = make(chan struct{}, 8)
		s.contextLog, s.contextErr = contextevent.Open(filepath.Join(s.ctx.Paths.State, "context-events"), contextevent.Options{Warn: func(msg string) {
			if s.logger != nil {
				s.logger.Warn(msg)
			}
		}})
	})
	return s.contextLog, s.contextErr
}
func (s *server) publishContext(source, operation, label string, count int, partial bool, unit string, ids ...string) {
	if count == 0 {
		return
	}
	scope := contextevent.Scope{Kind: "client", ClientLabel: label}
	if clientlabel.Shared(label) {
		scope = contextevent.Scope{Kind: "shared"}
	}
	log, e := s.contextJournal()
	if e == nil {
		var ev contextevent.Event
		ev, e = log.Append(contextevent.Event{Source: source, Operation: operation, Scope: scope, Result: contextevent.Result{CommittedCount: count, Partial: partial, Unit: unit}, ResourceIDs: ids})
		if e == nil {
			if s.logger != nil {
				s.logger.Info("context.event.persisted source=" + source + " eventId=" + ev.EventID)
			}
			if scope.Kind == "client" {
				s.dispatchContext(label, ev)
			}
		}
	}
	if e != nil && s.logger != nil {
		s.logger.Error("context.event.persist_failed source=" + source)
	}
}
func (s *server) handleContextEvents(w http.ResponseWriter, r *http.Request, a authResult) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, errBody("METHOD_NOT_ALLOWED", "GET required"))
		return
	}
	log, e := s.contextJournal()
	if e != nil {
		writeJSON(w, 503, errBody("CONTEXT_EVENTS_UNAVAILABLE", "event journal unavailable"))
		return
	}
	after := r.URL.Query().Get("after")
	last := r.Header.Get("Last-Event-ID")
	if last != "" {
		if after != "" && after != last {
			writeJSON(w, 400, errBody("INVALID_CURSOR", "conflicting cursors"))
			return
		}
		after = last
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 || limit > 500 {
			writeJSON(w, 400, errBody("INVALID_PARAMS", "limit must be 1..500"))
			return
		}
	}
	page, wake, e := log.Read(after, limit, a.scope())
	if e != nil {
		s.contextReadError(w, page, e)
		return
	}
	if r.URL.Path == "/context/events" {
		writeJSON(w, 200, page)
		return
	}
	// Relay's request/response bridge cannot carry an unbounded HTTP response.
	if a.authKind == "relay-api-key" {
		writeJSON(w, 400, errBody("STREAM_REQUIRES_HTTP", "use cursor polling over Relay"))
		return
	}
	if s.contextStreams.Add(1) > 32 {
		s.contextStreams.Add(-1)
		writeJSON(w, 429, errBody("TOO_MANY_STREAMS", "too many event streams"))
		return
	}
	defer s.contextStreams.Add(-1)
	if _, ok := w.(http.Flusher); !ok {
		writeJSON(w, 500, errBody("STREAM_UNAVAILABLE", "stream unsupported"))
		return
	}
	if after == "" {
		after = page.HighWatermark
		page, wake, e = log.Read(after, limit, a.scope())
		if e != nil {
			s.contextReadError(w, page, e)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	send := func(raw string) bool {
		_ = control.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, e := fmt.Fprint(w, raw); e != nil {
			return false
		}
		return control.Flush() == nil
	}
	defer control.SetWriteDeadline(time.Time{})
	if !send(": connected\n\n") {
		return
	}
	check := time.NewTicker(time.Second)
	defer check.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		// Revalidate before each page as well as while idle. API key revocation is
		// effective within one second; no new page is sent after revalidation fails.
		current, ok := s.authContext(r, r.URL.Path)
		if !ok || current != a {
			return
		}
		for _, ev := range page.Events {
			raw, _ := json.Marshal(ev)
			if !send("id: " + ev.Cursor + "\nevent: " + contextevent.Name + "\ndata: " + string(raw) + "\n\n") {
				return
			}
		}
		after = page.NextCursor
		if !page.HasMore {
			select {
			case <-r.Context().Done():
				return
			case <-wake:
			case <-check.C:
			case <-heartbeat.C:
				if !send(": heartbeat\n\n") {
					return
				}
			}
		}
		page, wake, e = log.Read(after, limit, a.scope())
		if e != nil {
			raw, _ := json.Marshal(map[string]string{"code": "CURSOR_EXPIRED", "highWatermark": page.HighWatermark})
			if !errors.Is(e, contextevent.ErrExpired) {
				raw = []byte(`{"code":"CONTEXT_EVENTS_UNAVAILABLE"}`)
			}
			send("event: error\ndata: " + string(raw) + "\n\n")
			return
		}
	}
}
func (s *server) contextReadError(w http.ResponseWriter, page contextevent.Page, e error) {
	if errors.Is(e, contextevent.ErrExpired) {
		writeJSON(w, 410, map[string]any{"ok": false, "error": map[string]string{"code": "CURSOR_EXPIRED"}, "highWatermark": page.HighWatermark})
		return
	}
	if errors.Is(e, contextevent.ErrCursor) {
		writeJSON(w, 400, errBody("INVALID_CURSOR", "invalid cursor"))
		return
	}
	writeJSON(w, 503, errBody("CONTEXT_EVENTS_UNAVAILABLE", "event journal unavailable"))
}

// Real-time delivery is bounded and best effort. Overflow remains replayable in
// the journal; slow network writes must never hold up a successful ingest.
func (s *server) dispatchContext(label string, ev contextevent.Event) {
	out := s.currentEgress()
	if out == nil {
		return
	}
	if _, noop := out.(NoopEgress); noop {
		return
	}
	select {
	case s.contextDelivery <- struct{}{}:
	default:
		if s.logger != nil {
			s.logger.Warn("context.event.delivery_skipped reason=busy")
		}
		return
	}
	go func() {
		defer func() { <-s.contextDelivery }()
		if s.logger != nil {
			s.logger.Info("context.event.delivery_attempt eventId=" + ev.EventID)
		}
		if proxy, ok := out.(*ProxyEgress); ok {
			body, err := json.Marshal(map[string]any{"event": contextevent.Name, "payload": ev, "clientLabel": label})
			if err == nil {
				proxy.deliver(contextevent.Name, body)
			}
			return
		}
		if err := out.PushEventTo(label, contextevent.Name, ev); err != nil && s.logger != nil {
			s.logger.Warn("context.event.delivery_failed eventId=" + ev.EventID)
		}
	}()
}
