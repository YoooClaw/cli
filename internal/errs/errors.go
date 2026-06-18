// Package errs 提供携带结构化错误码的 CLI 错误。
//
// 命令失败时顶层统一序列化为 {ok:false, error:{code,message,...}}（见 internal/output）。
package errs

import "fmt"

// 错误码常量，命名前缀统一 YOOOCLAW_*，进入半正式契约。
const (
	CodeUnknown                 = "YOOOCLAW_UNKNOWN"
	CodeInvalidArgument         = "YOOOCLAW_INVALID_ARGUMENT"
	CodeNotImplemented          = "YOOOCLAW_NOT_IMPLEMENTED"
	CodeDaemonNotRunning        = "YOOOCLAW_DAEMON_NOT_RUNNING"
	CodeDaemonAlreadyRunning    = "YOOOCLAW_DAEMON_ALREADY_RUNNING"
	CodeDaemonLifecycleMismatch = "YOOOCLAW_DAEMON_LIFECYCLE_MISMATCH"
	CodeUnauthorized            = "YOOOCLAW_UNAUTHORIZED"
	CodeConfigInvalid           = "YOOOCLAW_CONFIG_INVALID"
	CodeProfileNotFound         = "YOOOCLAW_PROFILE_NOT_FOUND"
	CodeImageNotReady           = "YOOOCLAW_IMAGE_NOT_READY"
	CodeNotFound                = "YOOOCLAW_NOT_FOUND"
	CodeAlreadyExists           = "YOOOCLAW_ALREADY_EXISTS"
	CodeStorageUnavailable      = "YOOOCLAW_STORAGE_UNAVAILABLE"
	CodeCredentialMissing       = "YOOOCLAW_CREDENTIAL_MISSING"
	CodeKeychainUnavailable     = "YOOOCLAW_KEYCHAIN_UNAVAILABLE"
	CodeAPIKeyLabelInvalid      = "YOOOCLAW_APIKEY_LABEL_INVALID"
	CodeAPIKeyLabelDuplicate    = "YOOOCLAW_APIKEY_LABEL_DUPLICATE"
	CodeAPIKeyLabelNotFound     = "YOOOCLAW_APIKEY_LABEL_NOT_FOUND"
	CodeKeychainMultiUnsupport  = "YOOOCLAW_KEYCHAIN_MULTI_UNSUPPORTED"
	CodeTunnelLabelNotFound     = "YOOOCLAW_TUNNEL_LABEL_NOT_FOUND"
	CodeNetworkError            = "YOOOCLAW_NETWORK_ERROR"
	CodeConfirmationRequired    = "YOOOCLAW_CONFIRMATION_REQUIRED"
	CodeNotInteractive          = "YOOOCLAW_NOT_INTERACTIVE"
)

// Error 携带结构化错误码、提示与进程退出码。
type Error struct {
	Code     string
	Message  string
	Details  map[string]any
	ExitCode int
}

func (e *Error) Error() string { return e.Message }

// Payload 序列化为统一错误 schema（code/message + 展开 details）。
func (e *Error) Payload() map[string]any {
	out := map[string]any{"code": e.Code, "message": e.Message}
	for k, v := range e.Details {
		out[k] = v
	}
	return out
}

// New 构造一个 Error，退出码默认 1。details 可选传入一个键值表。
func New(code, message string, details ...map[string]any) *Error {
	e := &Error{Code: code, Message: message, ExitCode: 1, Details: map[string]any{}}
	if len(details) > 0 && details[0] != nil {
		e.Details = details[0]
	}
	return e
}

// Newf 同 New，message 走 fmt.Sprintf。
func Newf(code, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...))
}

// WithHint 链式补充 hint 提示。
func (e *Error) WithHint(hint string) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details["hint"] = hint
	return e
}

// NotImplemented 返回脚手架占位错误。
func NotImplemented(command string) *Error {
	return New(CodeNotImplemented, fmt.Sprintf("命令 `%s` 尚未实现", command)).
		WithHint("该命令处于脚手架阶段，后续逐步落地")
}
