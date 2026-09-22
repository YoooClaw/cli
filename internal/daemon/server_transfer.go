package daemon

import (
	"encoding/json"
	"io"
	"net"
	"net/http"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/transfer"
)

// maxTransferBodyBytes 限制 /transfer 请求体：只有动作与路径/ID，不承载数据。
const maxTransferBodyBytes = 16 << 10

// handleTransfer 执行本地数据迁移（POST /transfer，供 `yoooclaw transfer` 调用）。
//
// 只接受本机调用方：请求能让 daemon 读取任意本地目录并写入存储，绝不能经
// Relay 隧道或 api-key（手机/扩展）触达。
func (s *server) handleTransfer(w http.ResponseWriter, r *http.Request, auth authResult) {
	if (auth.authKind != "local" && auth.authKind != "gateway-token") ||
		r.Header.Get(relay.InternalHTTPHeader) != "" || !isLoopbackRemote(r) {
		writeJSON(w, 403, errBody(errs.CodeUnauthorized, "transfer 只接受本机 CLI 调用"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxTransferBodyBytes+1))
	if err != nil || len(data) > maxTransferBodyBytes {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "请求体过大或读取失败"))
		return
	}
	var req transfer.Request
	if json.Unmarshal(data, &req) != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "请求体不是合法 JSON"))
		return
	}
	result, err := s.transfer.Handle(req)
	if err != nil {
		code := transfer.ErrorCode(err)
		if code == "" {
			code = "FAILED"
		}
		s.logger.Warn("[transfer] " + req.Action + " 失败：" + err.Error())
		writeJSON(w, 400, map[string]any{"ok": false, "error": map[string]any{"code": "YOOOCLAW_TRANSFER_" + code, "message": err.Error()}})
		return
	}
	if req.Action == "import" {
		if m, ok := result.(map[string]any); ok {
			s.logger.Info("[transfer] import " + req.LocalTransferID + " -> " + toString(m["state"]))
		}
	}
	writeJSON(w, 200, result)
}

func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}
