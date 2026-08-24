//go:build !windows

package filelockdiag

// Lookup 在非 Windows 平台不受支持。supported=false 让调用方保持安静，
// 避免为一个仅用于 Windows rename 失败的诊断输出无意义告警。
func Lookup(_ string) (holders []Holder, supported bool, err error) {
	return nil, false, nil
}
