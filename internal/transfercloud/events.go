package transfercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/transferjob"
)

type eventError struct{ permanent bool }

func (e *eventError) Error() string   { return "TRANSFER_EVENT_REPORT_FAILED" }
func (e *eventError) Permanent() bool { return e.permanent }

// ReportEvent uses the agreed fulfillment events endpoint; it adds no authorization API.
func (c *Client) ReportEvent(ctx context.Context, base string, event transferjob.Event) (reportErr error) {
	started := time.Now()
	observation := ReportObservation{Event: event.EventType, Progress: -1, Code: "validation_failed"}
	defer func() { finishReport(ctx, observation, started, reportErr) }()
	if err := c.ValidateProgress(ProgressOptions{TransferID: event.TransferID, BaseURL: base}); err != nil {
		return err
	}
	switch event.EventType {
	case "IMPORT_STARTED", "DOWNLOAD_COMPLETED", "IMPORT_SUCCEEDED", "IMPORT_FAILED":
	default:
		return &eventError{true}
	}
	if (event.EventType == "IMPORT_FAILED") != (event.ErrorMessage != "") || len([]rune(event.ErrorMessage)) > 512 {
		return &eventError{true}
	}
	body, _ := json.Marshal(event)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/events", bytes.NewReader(body))
	if err != nil {
		return &eventError{true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		observation.Code = transportFailure(err)
		return &eventError{false}
	}
	defer resp.Body.Close()
	observation.HTTPStatus = resp.StatusCode
	observation.Code = "http_error"
	permanent := func(code int) bool {
		return code == 400 || code == 401 || code == 403 || code == 404 || code == 409 || code == 410
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &eventError{permanent(resp.StatusCode)}
	}
	var bean struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&bean) != nil {
		observation.Code = "invalid_response"
		return &eventError{false}
	}
	observation.Code = bean.Code
	if !bean.Success || bean.Code != "000000" {
		return &eventError{bean.Code == "400" || bean.Code == "401" || bean.Code == "403" || bean.Code == "404" || bean.Code == "409" || bean.Code == "410"}
	}
	return nil
}
