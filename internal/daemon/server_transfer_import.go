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

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/transfer"
	"github.com/YoooClaw/cli/internal/transfercloud"
	"github.com/YoooClaw/cli/internal/transferjob"
)

type importDependencies struct {
	download func(context.Context, string, string, transfercloud.ProgressOptions) (error, error)
	handle   func(context.Context, transfer.Request) (any, error)
	log      func(string)
}

func executeTransferImport(ctx context.Context, req transferjob.Request, progress transfercloud.ProgressOptions, deps importDependencies, emit func(string) error) error {
	log := func(stage string) {
		if deps.log != nil {
			deps.log("[transfer] transferId=" + req.TransferID + " " + stage)
		}
	}
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
	log("download.completed")
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
	log("validation.completed")
	// Preview performs the existing archive/manifest/blob checks before reporting download completion.
	if err = emit("DOWNLOAD_COMPLETED"); err != nil {
		return err
	}
	log("import.writing")
	result, err := deps.handle(ctx, transfer.Request{Action: "import", LocalTransferID: local, PlanID: plan})
	if err != nil {
		return err
	}
	out, ok := result.(map[string]any)
	if !ok || out["state"] != "SUCCEEDED" {
		return errors.New("TRANSFER_IMPORT_PARTIAL")
	}
	return ctx.Err()
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
	base := os.Getenv("YOOOCLAW_TRANSFER_REPORT_BASE_URL")
	// Source and target use the same deployment-supplied fulfillment prefix.
	host := config.ResolveCloudHost(s.cfg)
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
			return client(label).ReportEvent(ctx, base, event)
		},
		Run: func(ctx context.Context, req transferjob.Request, label string, emit func(string) error) error {
			cloud := client(label)
			err := executeTransferImport(ctx, req, transfercloud.ProgressOptions{TransferID: req.TransferID, BaseURL: base, ReportLog: func(n int, ok bool) {
				line := fmt.Sprintf("[transfer] transferId=%s download_progress progress=%d reported=%t", req.TransferID, n, ok)
				if ok {
					s.logger.Info(line)
				} else {
					s.logger.Warn(line)
				}
			}}, importDependencies{
				log: s.logger.Info,
				download: func(ctx context.Context, id, path string, options transfercloud.ProgressOptions) (error, error) {
					downloadErr, reportErr := cloud.DownloadWithProgress(ctx, id, path, options)
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
	s.logger.Info("[transfer] import.worker_ready")
	s.transferImports = service
	return nil
}
func (s *server) handleTransferImportStart(w http.ResponseWriter, r *http.Request, auth authResult) {
	transferID := "unknown"
	if s.logger != nil {
		s.logger.Info("[transfer] import.request_received")
	}
	reply := func(status int, message string) {
		if s.logger != nil {
			line := fmt.Sprintf("[transfer] transferId=%s import.response status=%d code=%s", transferID, status, message)
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
