package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/transfer"
	"github.com/YoooClaw/cli/internal/transfercloud"
	"github.com/YoooClaw/cli/internal/transferjob"
)

type importDependencies struct {
	download func(context.Context, string, string, transfercloud.ProgressOptions) (error, error)
	handle   func(context.Context, transfer.Request) (any, error)
	log      func(string)
}

func executeTransferImport(ctx context.Context, req transferjob.Request, progress transfercloud.ProgressOptions, deps importDependencies, emit func(string) error) (runErr error) {
	log := func(stage string) {
		if deps.log != nil {
			deps.log("[transfer] transferId=" + req.TransferID + " " + stage)
		}
	}
	started := time.Now()
	stage, stageStarted := "download", started
	defer func() {
		if runErr != nil {
			log(fmt.Sprintf("%s.failed code=%s elapsedMs=%d totalElapsedMs=%d", stage, transferLogErrorCode(runErr), time.Since(stageStarted).Milliseconds(), time.Since(started).Milliseconds()))
		}
	}()
	log("download.started")
	dir, err := os.MkdirTemp("", "yoooclaw-async-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "migration.tar.gz")
	if err, _ = deps.download(ctx, req.FileTaskID, path, progress); err != nil {
		return err
	}
	if info, e := os.Stat(path); e == nil {
		log(fmt.Sprintf("download.completed bytes=%d elapsedMs=%d", info.Size(), time.Since(stageStarted).Milliseconds()))
	} else {
		log(fmt.Sprintf("download.completed elapsedMs=%d", time.Since(stageStarted).Milliseconds()))
	}
	stage, stageStarted = "validation", time.Now()
	log("validation.started")
	preview, err := deps.handle(ctx, transfer.Request{Action: "preview", File: path, CloudTaskID: req.FileTaskID})
	if err != nil {
		return err
	}
	p, ok := preview.(map[string]any)
	if !ok {
		return errors.New("TRANSFER_PLAN_INVALID")
	}
	local, _ := p["localTransferId"].(string)
	plan, _ := p["planId"].(string)
	if local == "" || plan == "" {
		return errors.New("TRANSFER_PLAN_INVALID")
	}
	log(fmt.Sprintf("validation.completed elapsedMs=%d", time.Since(stageStarted).Milliseconds()))
	stage, stageStarted = "download_completion_report", time.Now()
	// Preview performs the existing archive/manifest/blob checks before reporting download completion.
	if err = emit("DOWNLOAD_COMPLETED"); err != nil {
		return err
	}
	stage, stageStarted = "import", time.Now()
	log("import.writing")
	result, err := deps.handle(ctx, transfer.Request{Action: "import", LocalTransferID: local, PlanID: plan})
	if err != nil {
		return err
	}
	out, ok := result.(map[string]any)
	if !ok || out["state"] != "SUCCEEDED" {
		return errors.New("TRANSFER_IMPORT_PARTIAL")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	records, _ := out["results"].([]transfer.Result)
	files, _ := out["workspaceResults"].([]transfer.WorkspaceResult)
	log(fmt.Sprintf("import.written records=%d workspaceItems=%d elapsedMs=%d", len(records), len(files), time.Since(stageStarted).Milliseconds()))
	return nil
}

var transferLogCodeRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// Keep stable codes while excluding provider messages, signed URLs and user data.
func transferLogErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "TRANSFER_IMPORT_TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "TRANSFER_IMPORT_CANCELLED"
	}
	code := transfer.ErrorCode(err)
	var typed *errs.Error
	if code == "" && errors.As(err, &typed) {
		code = typed.Code
	}
	if code == "" && err != nil && strings.HasPrefix(err.Error(), "TRANSFER_") && transferLogCodeRE.MatchString(err.Error()) {
		code = err.Error()
	}
	if transferLogCodeRE.MatchString(code) {
		return code
	}
	return "TRANSFER_STAGE_FAILED"
}
func (s *server) transferKey(label string) string {
	for _, e := range s.snapshotCreds().Entries {
		if e.Label == label {
			return e.Key
		}
	}
	return ""
}
func (s *server) startTransferImports() error {
	host := config.ResolveCloudHost(s.cfg)
	base := transfercloud.New("configuration-check", host).ReportBaseURL(os.Getenv("YOOOCLAW_TRANSFER_REPORT_BASE_URL"))
	keys := map[string]string{}
	for _, e := range s.snapshotCreds().Entries {
		keys[e.Label] = e.Key
	}
	identity := func(label string) string {
		key := keys[label]
		if key == "" || s.transferKey(label) != key {
			return ""
		}
		sum := sha256.Sum256([]byte(host + "\x00" + base + "\x00" + label + "\x00" + key))
		return hex.EncodeToString(sum[:])
	}
	client := func(label string) *transfercloud.Client { return transfercloud.New(keys[label], host) }
	// Validate configuration without requiring a particular first account.
	if err := transfercloud.New("configuration-check", host).ValidateProgress(transfercloud.ProgressOptions{TransferID: "config", BaseURL: base}); err != nil {
		return err
	}
	service, err := transferjob.New(filepath.Join(s.ctx.Paths.Dir, "transfer-import-jobs"), transferjob.Dependencies{
		Timeout: transfer.Timeout(), Identity: identity, Warn: s.logger.Warn, Info: s.logger.Info,
		Report: func(ctx context.Context, label string, event transferjob.Event) error {
			return client(label).ReportEvent(s.transferReportContext(ctx, event.TransferID), base, event)
		},
		Run: func(ctx context.Context, req transferjob.Request, label string, emit func(string) error) error {
			cloud := client(label)
			err := executeTransferImport(ctx, req, transfercloud.ProgressOptions{TransferID: req.TransferID, BaseURL: base, DownloadLog: func(written, total int64, elapsed, idle time.Duration) {
				s.logger.Info(fmt.Sprintf("[transfer] transferId=%s download.activity bytes=%d totalBytes=%d elapsedMs=%d idleMs=%d", req.TransferID, written, total, elapsed.Milliseconds(), idle.Milliseconds()))
			}}, importDependencies{
				log: s.logger.Info,
				download: func(ctx context.Context, id, path string, options transfercloud.ProgressOptions) (error, error) {
					downloadErr, reportErr := cloud.DownloadWithProgress(s.transferReportContext(ctx, req.TransferID), id, path, options)
					if reportErr != nil {
						s.logger.Warn("[transfer] transferId=" + req.TransferID + " TRANSFER_PROGRESS_REPORT_FAILED")
					}
					return downloadErr, reportErr
				}, handle: s.transfer.HandleContext,
			}, emit)
			return err
		},
	})
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	s.logger.Info(fmt.Sprintf("[transfer] import.worker_ready pid=%d hostname=%q profile=%q jobsDir=%q", os.Getpid(), hostname, s.ctx.Profile, filepath.Join(s.ctx.Paths.Dir, "transfer-import-jobs")))
	s.transferImports = service
	return nil
}
func (s *server) handleTransferImportStart(w http.ResponseWriter, r *http.Request, auth authResult) {
	transferID := "unknown"
	requestID := r.Header.Get(relay.InternalRequestIDHeader)
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	requestContext := fmt.Sprintf("requestId=%q client=%q pid=%d", requestID, auth.clientLabel, os.Getpid())
	if s.logger != nil {
		s.logger.Info("[transfer] import.request_received " + requestContext)
	}
	reply := func(status int, message string) {
		if s.logger != nil {
			line := fmt.Sprintf("[transfer] transferId=%s import.response status=%d code=%s %s", transferID, status, message, requestContext)
			if status == 200 {
				s.logger.Info(line)
			} else {
				s.logger.Warn(line)
			}
		}
		code := "000000"
		if status != 200 {
			code = http.StatusText(status)
		}
		writeJSON(w, status, map[string]any{"success": status == 200, "code": code, "message": message, "data": nil})
	}
	// Reuse authenticated Relay identity; the local arbitrary-path API stays local.
	if auth.authKind != "relay-api-key" || s.transferKey(auth.clientLabel) == "" {
		reply(403, "TRANSFER_FORBIDDEN")
		return
	}
	if s.transferImports == nil {
		reply(503, "TRANSFER_NOT_READY")
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
	if readErr != nil || len(raw) > 4096 {
		reply(400, "TRANSFER_INVALID_REQUEST")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var req transferjob.Request
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || !req.Valid() {
		reply(400, "TRANSFER_INVALID_REQUEST")
		return
	}
	transferID = req.TransferID
	if s.logger != nil {
		s.logger.Info(fmt.Sprintf("[transfer] transferId=%s import.request_validated fileTaskId=%s %s", transferID, req.FileTaskID, requestContext))
	}
	err := s.transferImports.Accept(req, auth.clientLabel)
	switch {
	case err == nil:
		reply(200, "success")
	case errors.Is(err, transferjob.ErrConflict), errors.Is(err, transferjob.ErrBusy):
		reply(409, err.Error())
	default:
		reply(503, "TRANSFER_NOT_READY")
	}
}

func (s *server) transferReportContext(ctx context.Context, transferID string) context.Context {
	return transfercloud.WithReportLog(ctx, func(o transfercloud.ReportObservation) {
		line := fmt.Sprintf("[transfer] transferId=%s report.result event=%s httpStatus=%d code=%q success=%t elapsedMs=%d", transferID, o.Event, o.HTTPStatus, o.Code, o.Success, o.Elapsed.Milliseconds())
		if o.Event == "download_progress" {
			line += fmt.Sprintf(" progress=%d attempt=%d", o.Progress, o.Attempt)
		}
		if o.Success {
			s.logger.Info(line)
		} else {
			s.logger.Warn(line)
		}
	})
}
