// Package creds 提供凭据抽象引用（*Ref）解析与写入，以及 api-key 分层解析。
//
// 支持 scheme：
//   - env:<VAR>                  从环境变量读（最高优先级覆盖）
//   - file:<path>#<jsonField>    从 JSON 文件某字段读/写（默认落点，0600）
//   - keychain:<service>/<acct>  从 OS keychain 读/写（可选加固）
//   - inline:<base64>            直接内嵌
package creds

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

// ResolvedRef 是 ref 解析结果。
type ResolvedRef struct {
	Value    string
	Source   string // env | file | keychain | inline
	Location string
}

func expandHome(path string) string {
	if path == "~" {
		h, _ := os.UserHomeDir()
		return h
	}
	if strings.HasPrefix(path, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, path[2:])
	}
	return path
}

func parseFileRef(rest string) (path, field string, err error) {
	hashIdx := strings.LastIndex(rest, "#")
	if hashIdx < 0 {
		return "", "", errs.New(errs.CodeConfigInvalid, "file: 引用缺少 #字段："+rest).
			WithHint("格式应为 file:<path>#<jsonField>")
	}
	return expandHome(rest[:hashIdx]), rest[hashIdx+1:], nil
}

func parseKeychainRef(rest string) (service, account string, err error) {
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return "", "", errs.New(errs.CodeConfigInvalid, "keychain: 引用缺少 /account："+rest).
			WithHint("格式应为 keychain:<service>/<account>")
	}
	return rest[:slashIdx], rest[slashIdx+1:], nil
}

// ResolveRef 解析一个 ref，返回当前值（可能为空）与来源。
func ResolveRef(ref string) (ResolvedRef, error) {
	colonIdx := strings.Index(ref, ":")
	if colonIdx < 0 {
		return ResolvedRef{}, errs.New(errs.CodeConfigInvalid, "非法凭据引用："+ref).
			WithHint("支持 env: / file: / keychain: / inline:")
	}
	scheme, rest := ref[:colonIdx], ref[colonIdx+1:]

	switch scheme {
	case "env":
		return ResolvedRef{Source: "env", Value: strings.TrimSpace(os.Getenv(rest)), Location: rest}, nil
	case "file":
		path, field, err := parseFileRef(rest)
		if err != nil {
			return ResolvedRef{}, err
		}
		var data map[string]any
		if _, err := fsutil.ReadJSON(path, &data); err != nil {
			return ResolvedRef{}, err
		}
		val, _ := data[field].(string)
		return ResolvedRef{Source: "file", Value: val, Location: path}, nil
	case "keychain":
		service, account, err := parseKeychainRef(rest)
		if err != nil {
			return ResolvedRef{}, err
		}
		r := keychainGetFn(service, account)
		return ResolvedRef{Source: "keychain", Value: r.Value, Location: service + "/" + account}, nil
	case "inline":
		decoded, _ := base64.StdEncoding.DecodeString(rest)
		return ResolvedRef{Source: "inline", Value: string(decoded)}, nil
	default:
		return ResolvedRef{}, errs.New(errs.CodeConfigInvalid, "不支持的凭据引用 scheme："+scheme).
			WithHint("支持 env: / file: / keychain: / inline:")
	}
}

// WriteRef 把值写入 ref 指向的后端。env/inline 不可持久写入（报错）。
func WriteRef(ref, value string) (ResolvedRef, error) {
	colonIdx := strings.Index(ref, ":")
	if colonIdx < 0 {
		return ResolvedRef{}, errs.New(errs.CodeConfigInvalid, "非法凭据引用："+ref)
	}
	scheme, rest := ref[:colonIdx], ref[colonIdx+1:]

	switch scheme {
	case "file":
		path, field, err := parseFileRef(rest)
		if err != nil {
			return ResolvedRef{}, err
		}
		data := map[string]any{}
		if _, err := fsutil.ReadJSON(path, &data); err != nil {
			return ResolvedRef{}, err
		}
		if data == nil {
			data = map[string]any{}
		}
		data[field] = value
		if err := fsutil.WriteJSON(path, data, fsutil.SecretFileMode); err != nil {
			return ResolvedRef{}, err
		}
		return ResolvedRef{Source: "file", Value: value, Location: path}, nil
	case "keychain":
		service, account, err := parseKeychainRef(rest)
		if err != nil {
			return ResolvedRef{}, err
		}
		if !keychainSetFn(service, account, value) {
			return ResolvedRef{}, errs.New(errs.CodeKeychainUnavailable, "keychain 写入失败："+service+"/"+account).
				WithHint("当前平台可能没有可用的 keychain 工具，请改用 file: 引用")
		}
		return ResolvedRef{Source: "keychain", Value: value, Location: service + "/" + account}, nil
	case "env":
		return ResolvedRef{}, errs.New(errs.CodeInvalidArgument, "env: 引用无法持久化写入；请改用 file: 或 keychain:")
	case "inline":
		return ResolvedRef{}, errs.New(errs.CodeInvalidArgument, "inline: 引用需直接写进 config，不通过 writeRef")
	default:
		return ResolvedRef{}, errs.New(errs.CodeConfigInvalid, "不支持的凭据引用 scheme："+scheme)
	}
}
