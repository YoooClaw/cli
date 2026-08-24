// Package filelockdiag 提供文件占用者诊断。它只做只读查询，绝不关闭句柄或终止进程。
package filelockdiag

// Holder 描述一个由操作系统识别出的文件占用进程。
type Holder struct {
	PID             uint32
	Application     string
	Service         string
	ApplicationType uint32
	Restartable     bool
}
