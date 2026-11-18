package transfercloud

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

// Keep diagnostics local: Error/Unwrap preserve the existing public contract.
// Never retain SDK Error(), response bodies, request targets or credentials.
type uploadDiagnosticError struct {
	err    error
	detail string
}

func (e *uploadDiagnosticError) Error() string      { return e.err.Error() }
func (e *uploadDiagnosticError) Unwrap() error      { return e.err }
func (e *uploadDiagnosticError) Diagnostic() string { return e.detail }

var uploadDiagnosticTokenRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func uploadDiagnosticToken(s string) string {
	if !uploadDiagnosticTokenRE.MatchString(s) {
		return "unknown"
	}
	return s
}

func uploadFailureDetail(stage string, attempt int, started time.Time, err error) string {
	detail := fmt.Sprintf("stage=%s attempt=%d elapsed_ms=%d", stage, attempt, time.Since(started).Milliseconds())
	var op *oss.OperationError
	if errors.As(err, &op) {
		detail += " operation=" + uploadDiagnosticToken(op.Operation())
	}
	var service *oss.ServiceError
	if errors.As(err, &service) {
		return detail + fmt.Sprintf(" http_status=%d oss_code=%s oss_request_id=%s oss_ec=%s",
			service.StatusCode, uploadDiagnosticToken(service.Code), uploadDiagnosticToken(service.RequestID), uploadDiagnosticToken(service.EC))
	}
	reason := transportFailure(err)
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		detail += fmt.Sprintf(" http_status=%d", httpErr.StatusCode)
		reason = "http_error"
	}
	var coded *errs.Error
	if errors.As(err, &coded) {
		detail += " code=" + uploadDiagnosticToken(coded.Code)
	}
	var client *oss.ClientError
	if errors.As(err, &client) {
		detail += " sdk_code=" + uploadDiagnosticToken(client.Code)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		detail += fmt.Sprintf(" dns_error=true dns_not_found=%t", dns.IsNotFound)
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		detail += fmt.Sprintf(" errno=%d", errno)
	}
	return detail + " reason=" + reason + fmt.Sprintf(" error_type=%T", err)
}

func withUploadDiagnostics(err error, details []string) error {
	if err == nil || len(details) == 0 {
		return err
	}
	return &uploadDiagnosticError{err: err, detail: strings.Join(details, " | ")}
}
