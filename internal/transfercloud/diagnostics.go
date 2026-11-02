package transfercloud

import (
	"context"
	"errors"
	"net"
	"regexp"
	"time"
)

// ReportObservation deliberately excludes request URLs, headers and response bodies.
type ReportObservation struct {
	Event      string
	Progress   int
	HTTPStatus int
	Attempt    int
	Code       string
	Success    bool
	Elapsed    time.Duration
}
type reportLogKey struct{}
type reportAttemptKey struct{}

func WithReportLog(ctx context.Context, log func(ReportObservation)) context.Context {
	return context.WithValue(ctx, reportLogKey{}, log)
}

var diagnosticCodeRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func finishReport(ctx context.Context, observation ReportObservation, started time.Time, err error) {
	log, _ := ctx.Value(reportLogKey{}).(func(ReportObservation))
	if log == nil {
		return
	}
	observation.Attempt, _ = ctx.Value(reportAttemptKey{}).(int)
	observation.Elapsed = time.Since(started)
	observation.Success = err == nil
	if !diagnosticCodeRE.MatchString(observation.Code) {
		observation.Code = "unknown"
	}
	log(observation)
}
func transportFailure(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	return "transport_error"
}
