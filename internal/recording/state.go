package recording

import "fmt"

const (
	StatusReceivingFailed  = "receiving_failed"
	StatusReceiving        = "receiving"
	StatusPendingOSSUpload = "pending_oss_upload"
	StatusUploadingOSS     = "uploading_oss"
	StatusOSSUploaded      = "oss_uploaded"
	StatusSynced           = "synced"
	StatusTranscribing     = "transcribing"
	StatusTranscribeFailed = "transcribe_failed"
	StatusTranscribed      = "transcribed"
)

// 音频下载状态与转写状态相互独立。result.write 可以先写入转录结果，再异步把
// OSS 音频落到本地；不能用 transcribed 代替音频已经下载成功。
const (
	AudioStatusPending     = "pending"
	AudioStatusDownloading = "downloading"
	AudioStatusDownloaded  = "downloaded"
	AudioStatusFailed      = "failed"
)

var validTransitions = map[string]map[string]bool{
	StatusReceivingFailed:  {StatusReceiving: true},
	StatusReceiving:        {StatusPendingOSSUpload: true},
	StatusPendingOSSUpload: {StatusUploadingOSS: true},
	StatusUploadingOSS:     {StatusOSSUploaded: true},
	StatusOSSUploaded:      {StatusSynced: true},
	StatusSynced:           {StatusTranscribing: true},
	StatusTranscribing:     {StatusTranscribed: true, StatusTranscribeFailed: true},
	StatusTranscribeFailed: {StatusTranscribing: true},
	StatusTranscribed:      {StatusTranscribing: true},
}

// TransitionError 表示非法状态流转。
type TransitionError struct {
	From string
	To   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("非法状态转换: %s -> %s", e.From, e.To)
}

// ValidateTransition 校验状态转换是否合法。
func ValidateTransition(from, to string) error {
	if allowed := validTransitions[from]; allowed != nil && allowed[to] {
		return nil
	}
	return &TransitionError{From: from, To: to}
}

// CanStartTranscription 判断当前状态是否允许触发 ASR。
func CanStartTranscription(status string) bool {
	return status == StatusSynced || status == StatusTranscribeFailed || status == StatusTranscribed
}

// IsRecordingStatus 判断字符串是否为已知录音传输状态。
func IsRecordingStatus(status string) bool {
	if _, ok := validTransitions[status]; ok {
		return true
	}
	return status == StatusTranscribed
}
