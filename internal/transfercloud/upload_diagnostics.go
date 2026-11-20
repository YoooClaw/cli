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
// Keep only sanitized, bounded descriptions; never retain SDK snapshots or headers.
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

func uploadFailureDetail(stage string, attempt int, started time.Time, err error, secrets ...string) string {
	detail := fmt.Sprintf("stage=%s attempt=%d elapsed_ms=%d", stage, attempt, time.Since(started).Milliseconds())
	var op *oss.OperationError
	if errors.As(err, &op) {
		detail += " operation=" + uploadDiagnosticToken(op.Operation())
	}
	var service *oss.ServiceError
	if errors.As(err, &service) {
		return detail + fmt.Sprintf(" http_status=%d oss_code=%s oss_request_id=%s oss_ec=%s oss_message=%q",
			service.StatusCode, diagnosticCode(service.Code), uploadDiagnosticToken(service.RequestID), uploadDiagnosticToken(service.EC), redactDiagnostic(service.Message, secrets...))
	}
	reason := transportFailure(err)
	if reason == "transport_error" {
		var network net.Error
		if !errors.As(err, &network) {
			reason = "unknown_error"
		}
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		detail += fmt.Sprintf(" http_status=%d service_detail=%q", httpErr.StatusCode, redactDiagnostic(httpErr.Detail, secrets...))
		reason = "http_error"
	}
	var coded *errs.Error
	if errors.As(err, &coded) {
		detail += " code=" + diagnosticCode(coded.Code)
		if reason == "unknown_error" {
			reason = "service_error"
		}
	}
	var client *oss.ClientError
	if errors.As(err, &client) {
		detail += " sdk_code=" + diagnosticCode(client.Code)
		if reason == "unknown_error" {
			reason = "sdk_error"
		}
		detail += fmt.Sprintf(" sdk_message=%q", redactDiagnostic(client.Message, secrets...))
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

// Snapshot credential generations under the provider lock: a failed multipart
// request may have used credentials from before a concurrent refresh.
func (p *uploadCredentials) failureDetail(stage string, attempt int, started time.Time, err error) string {
	p.mu.Lock()
	secrets := append([]string{p.cloud.apiKey}, p.previousSecrets...)
	c := p.task.Credentials
	secrets = append(secrets, c.AccessKeyID, c.AccessKeySecret, c.SecurityToken)
	p.mu.Unlock()
	return uploadFailureDetail(stage, attempt, started, err, secrets...)
}
