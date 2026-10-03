package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// /local-services/<name>/<rest>：把 Relay 下行的请求转给本机登记过的服务。
//
// 网页端（如 agent.yoooclaw.com）只能经 messageBridge 找到用户的这台机器，而
// messageBridge 只认插件连接；本机其他程序（如 clawpilot-thread 的 daemon）只监听
// 回环地址、不对外。这里做那一跳：
//
//   - 服务自己写登记文件 <root>/local-services/<name>.json：
//     {"baseUrl":"http://127.0.0.1:47831","token":"..."}，权限 600。daemon 每次请求现读，
//     服务换端口、换令牌不用重启 daemon。
//   - baseUrl 只接受回环地址，登记文件被改也转不到别的主机。
//   - messageBridge 只发 POST，所以请求体是信封 {"method":"GET","body":"..."}；
//     转发时用服务的令牌作 Bearer，不透传 Relay 这一侧的任何鉴权头。
//   - 响应原样带回状态码、Content-Type 与正文；不支持流式响应（messageBridge 的
//     HTTP 入口等的是一次性结果）。
const (
	localServiceTimeout     = 25 * time.Second // messageBridge 等插件 30s，留出余量
	maxLocalServiceRespSize = 8 << 20
)

var localServiceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type localServiceEntry struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
}

type localServiceEnvelope struct {
	Method string  `json:"method"`
	Body   *string `json:"body"`
}

var localServiceClient = &http.Client{
	Timeout: localServiceTimeout,
	// 登记的服务只在本机，不跟随重定向，免得被引到别处。
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *server) handleLocalService(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, errBody("METHOD_NOT_ALLOWED", "只接受 POST 信封"))
		return
	}
	rest := strings.TrimPrefix(path, "/local-services/")
	name, sub, _ := strings.Cut(rest, "/")
	if !localServiceNamePattern.MatchString(name) {
		writeJSON(w, 404, errBody(errs.CodeNotFound, "未知服务"))
		return
	}
	entry, err := readLocalService(name)
	if err != nil {
		writeJSON(w, 503, errBody("SERVICE_UNAVAILABLE", "服务未登记："+name))
		return
	}
	var env localServiceEnvelope
	if !decodeBody(w, r, &env) {
		return
	}
	method := strings.ToUpper(strings.TrimSpace(env.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "method 只支持 GET / POST"))
		return
	}
	target := entry.BaseURL + "/" + sub
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	var body io.Reader
	if method == http.MethodPost && env.Body != nil {
		body = strings.NewReader(*env.Body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, target, body)
	if err != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "无效的路径"))
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if entry.Token != "" {
		req.Header.Set("Authorization", "Bearer "+entry.Token)
	}
	resp, err := localServiceClient.Do(req)
	if err != nil {
		s.logger.Warn("local-services: " + name + " 请求失败：" + err.Error())
		writeJSON(w, 502, errBody("SERVICE_UNAVAILABLE", "服务没有响应："+name))
		return
	}
	defer resp.Body.Close()
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "不支持流式响应"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLocalServiceRespSize+1))
	if err != nil {
		writeJSON(w, 502, errBody("SERVICE_UNAVAILABLE", "读取服务响应失败"))
		return
	}
	if len(raw) > maxLocalServiceRespSize {
		writeJSON(w, 502, errBody("RESPONSE_TOO_LARGE", "服务响应过大"))
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

func readLocalService(name string) (localServiceEntry, error) {
	raw, err := os.ReadFile(filepath.Join(paths.LocalServicesDir(), name+".json"))
	if err != nil {
		return localServiceEntry{}, err
	}
	var entry localServiceEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return localServiceEntry{}, err
	}
	u, err := url.Parse(strings.TrimRight(entry.BaseURL, "/"))
	if err != nil || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return localServiceEntry{}, errors.New("baseUrl 无效")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return localServiceEntry{}, errors.New("baseUrl 只能是回环地址")
	}
	entry.BaseURL = u.String()
	return entry, nil
}
