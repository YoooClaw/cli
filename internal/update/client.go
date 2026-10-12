// Package update implements the standalone CLI update protocol. It does not own
// Hermes configuration or credentials; each attempt is bound to one account and host.
package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/version"
)

const CheckPath = "/api/ota/client/plugin/check-update"
const ResultPath = "/api/ota/client/plugin/update-result"
const ReportPath = "/api/message/messageBridge/plugin/update-report"

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func fail(code, message string) error { return &Error{code, message} }
func Hash(data []byte) string         { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type Identity struct {
	BaseURL        string `json:"baseUrl"`
	KeyHash        string `json:"keyHash"`
	DeviceID       string `json:"deviceId"`
	CurrentVersion string `json:"currentVersion"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	Profile        string `json:"profile"`
	Root           string `json:"root"`
}

func Platform(goos, goarch string) (string, string, error) {
	platform := goos
	if platform == "darwin" {
		platform = "macos"
	}
	arch := goarch
	if arch == "amd64" {
		arch = "x64"
	}
	if (platform != "macos" && platform != "linux" && platform != "windows") || (arch != "arm64" && arch != "x64") {
		return "", "", fail("UNSUPPORTED_PLATFORM", "当前系统或架构不支持更新")
	}
	return platform, arch, nil
}
func NewIdentity(p paths.Paths) (Identity, error) {
	cfg, err := config.Load(p)
	if err != nil {
		return Identity{}, err
	}
	key := creds.ResolveAPIKey().Value
	if key == "" {
		return Identity{}, fail("AUTH_REQUIRED", "更新需要 API Key")
	}
	platform, arch, err := Platform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Identity{}, err
	}
	root := filepath.Dir(filepath.Dir(p.Dir))
	dir := filepath.Join(root, "update")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Identity{}, err
	}
	release, err := lock(filepath.Join(dir, "identity.lock"))
	if err != nil {
		return Identity{}, err
	}
	defer release()
	idPath := filepath.Join(dir, "device-id")
	raw, err := os.ReadFile(idPath)
	if os.IsNotExist(err) {
		raw = []byte(ID())
		err = fsutil.WriteAtomic(idPath, raw, 0600)
	}
	if err != nil {
		return Identity{}, err
	}
	if len(strings.TrimSpace(string(raw))) != 32 {
		return Identity{}, fail("INVALID_IDENTITY", "更新设备标识损坏")
	}
	return Identity{"https://" + config.ResolveCloudHost(cfg), Hash([]byte(key)), strings.TrimSpace(string(raw)), version.Version, platform, arch, p.Profile, root}, nil
}
func (id Identity) Directory() string {
	return filepath.Join(id.Root, "update", Hash([]byte(id.BaseURL)), id.KeyHash, id.Profile)
}
func (id Identity) Key() (string, error) {
	cfg, err := config.Load(paths.ForRoot(id.Root, id.Profile))
	if err != nil {
		return "", err
	}
	if "https://"+config.ResolveCloudHost(cfg) != id.BaseURL || paths.RootDir() != id.Root {
		return "", fail("ENV_CHANGED", "更新环境已改变，请重新检查")
	}
	key := creds.ResolveAPIKey().Value
	if key == "" || Hash([]byte(key)) != id.KeyHash {
		return "", fail("AUTH_CHANGED", "更新账号已改变，请重新检查")
	}
	return key, nil
}

type Artifact struct {
	TargetVersion string `json:"targetVersion"`
	DownloadURL   string `json:"downloadUrl"`
	FileSize      int64  `json:"fileSize"`
	FileHash      string `json:"fileHash"`
	ReleaseNotes  string `json:"releaseNotes,omitempty"`
}
type Client struct {
	HTTP *http.Client
	Key  func() (string, error)
}

func NewClient(id Identity) *Client {
	return &Client{HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.Host != via[0].URL.Host {
			return fail("INVALID_REDIRECT", "鉴权请求不允许跨主机重定向")
		}
		return nil
	}}, Key: id.Key}
}
func (c *Client) request(ctx context.Context, id Identity, path string, body map[string]any, out any) error {
	key, err := c.Key()
	if err != nil {
		return err
	}
	body["component"] = "CLI"
	body["deviceId"] = id.DeviceID
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", id.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fail("INVALID_ENDPOINT", "无效的更新服务地址")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fail("NETWORK_ERROR", "更新服务请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fail(fmt.Sprintf("HTTP_%d", resp.StatusCode), "更新服务拒绝请求")
	}
	var envelope struct {
		Success bool            `json:"success"`
		Code    string          `json:"code"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return fail("INVALID_RESPONSE", "更新响应格式错误")
	}
	if !envelope.Success || envelope.Code != "000000" {
		code := envelope.Code
		if !regexp.MustCompile(`^[A-Z0-9_]{1,64}$`).MatchString(code) {
			code = "OTA_FAILED"
		}
		return fail(code, "更新服务业务请求失败")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fail("INVALID_RESPONSE", "更新业务数据格式错误")
	}
	return nil
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-beta\.(0|[1-9][0-9]*))?$`)

func Compare(a, b string) (int, error) {
	pa, pb := versionPattern.FindStringSubmatch(a), versionPattern.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return 0, fail("INVALID_VERSION", "仅支持 x.y.z 或 x.y.z-beta.N 版本")
	}
	cmp := func(x, y string) int {
		if len(x) < len(y) {
			return -1
		}
		if len(x) > len(y) {
			return 1
		}
		return strings.Compare(x, y)
	}
	for i := 1; i <= 3; i++ {
		if d := cmp(pa[i], pb[i]); d != 0 {
			return d, nil
		}
	}
	if pa[4] == pb[4] {
		return 0, nil
	}
	if pa[4] == "" {
		return 1, nil
	}
	if pb[4] == "" {
		return -1, nil
	}
	return cmp(pa[4], pb[4]), nil
}
func (c *Client) Check(ctx context.Context, id Identity, target string) (*Artifact, error) {
	if _, err := Compare(id.CurrentVersion, id.CurrentVersion); err != nil {
		return nil, err
	}
	body := map[string]any{"currentVersion": id.CurrentVersion, "platform": id.Platform + "-" + id.Arch}
	if target != "" {
		body["targetVersion"] = target
	}
	var data struct {
		UpdateAvailable *bool `json:"updateAvailable"`
		Artifact
	}
	if err := c.request(ctx, id, CheckPath, body, &data); err != nil {
		return nil, err
	}
	if data.UpdateAvailable == nil {
		return nil, fail("INVALID_OFFER", "缺少更新决策")
	}
	if !*data.UpdateAvailable {
		return nil, nil
	}
	a := data.Artifact
	u, err := url.Parse(a.DownloadURL)
	comparison, verErr := Compare(a.TargetVersion, id.CurrentVersion)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || verErr != nil || comparison <= 0 || (target != "" && a.TargetVersion != target) || a.FileSize <= 0 || a.FileSize > 512<<20 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(a.FileHash) {
		return nil, fail("INVALID_OFFER", "版本或制品信息无效")
	}
	return &a, nil
}
func (c *Client) Recheck(ctx context.Context, id Identity, a Artifact) error {
	_, err := c.refresh(ctx, id, a)
	return err
}
func (c *Client) refresh(ctx context.Context, id Identity, a Artifact) (*Artifact, error) {
	fresh, err := c.Check(ctx, id, a.TargetVersion)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, fail("OFFER_REVOKED", "当前版本已无安装资格")
	}
	if fresh.FileHash != a.FileHash || fresh.FileSize != a.FileSize {
		return nil, fail("ARTIFACT_CHANGED", "目标制品发生变化，请重新检查")
	}
	return fresh, nil
}
func (c *Client) Download(ctx context.Context, a Artifact, destination string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.DownloadURL, nil)
	if err != nil {
		return fail("INVALID_OFFER", "下载地址错误")
	}
	// Never send API credentials to the artifact host, including after redirects.
	httpClient := *c.HTTP
	httpClient.Timeout = 10 * time.Minute
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return fail("INVALID_REDIRECT", "下载重定向不安全")
		}
		return nil
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fail("DOWNLOAD_FAILED", "下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fail("DOWNLOAD_FAILED", "下载返回 HTTP "+strconv.Itoa(resp.StatusCode))
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, a.FileSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return fail("DOWNLOAD_FAILED", "写入安装包失败")
	}
	if n != a.FileSize {
		_ = os.Remove(destination)
		return fail("SIZE_MISMATCH", "安装包大小不一致")
	}
	if hex.EncodeToString(hash.Sum(nil)) != a.FileHash {
		_ = os.Remove(destination)
		return fail("HASH_MISMATCH", "安装包 SHA-256 不一致")
	}
	return nil
}
