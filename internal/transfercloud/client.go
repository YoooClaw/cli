// Package transfercloud implements the migration task API. Credentials and signed
// URLs stay in memory; only task IDs are returned to agents or persisted in plans.
package transfercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/envhost"
	"github.com/YoooClaw/cli/internal/errs"
)

const MaxBytes int64 = 5 << 30
const apiPath = "/api/device/file/plugin"

var taskRE = regexp.MustCompile(`^[a-f0-9]{32}$`)
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var regionRE = regexp.MustCompile(`^(?:oss-)?[a-z0-9-]+$`)
var endpointRE = regexp.MustCompile(`^https://oss-[a-z0-9-]+\.aliyuncs\.com/?$`)
var objectKeyRE = regexp.MustCompile(`^users/data-transfer/[0-9]{8}/([a-f0-9]{32})\.tar\.gz$`)
var codeRE = regexp.MustCompile(`^[0-9]{3,6}$`)
var ossHostRE = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9-]*\.)?oss-[a-z0-9-]+\.aliyuncs\.com$`)

func ValidTaskID(id string) bool         { return taskRE.MatchString(id) }
func failure(code, message string) error { return errs.New("YOOOCLAW_TRANSFER_"+code, message) }

// Number accepts the decimal strings used by the server's Java Long serializer.
type Number int64

func (n *Number) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) > 0 && s[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return fmt.Errorf("invalid nonnegative integer")
	}
	*n = Number(v)
	return nil
}

type UploadTask struct {
	TaskID      string `json:"taskId"`
	ObjectKey   string `json:"objectKey"`
	Bucket      string `json:"bucket"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Credentials struct {
		AccessKeyID     string `json:"accessKeyId"`
		AccessKeySecret string `json:"accessKeySecret"`
		SecurityToken   string `json:"securityToken"`
		Expiration      string `json:"expiration"`
	} `json:"credentials"`
}
type Receipt struct {
	ObjectKey string `json:"objectKey"`
	TaskID    string `json:"taskId"`
	FileSize  Number `json:"fileSize"`
	Status    string `json:"status"`
}
type Client struct {
	apiKey, baseURL string
	http            *http.Client
}

func New(apiKey, host string) *Client {
	host = envhost.Normalize(host)
	if host == "" {
		host = envhost.Host()
	}
	return &Client{apiKey: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(apiKey), "Bearer ")), baseURL: "https://" + host + apiPath, http: secureHTTPClient()}
}
func secureHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (c *Client) CheckAuth() error {
	if c.apiKey == "" {
		return failure("API_KEY_REQUIRED", "请先配置 CLI 的 API Key")
	}
	return nil
}
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	if err := c.CheckAuth(); err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return failure("CLOUD_REQUEST_INVALID", "迁移请求无效")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return failure("CLOUD_REQUEST_INVALID", "迁移服务地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", c.apiKey)
	// Scope belongs to the API key; no scope header is sent.
	resp, err := c.http.Do(req)
	if err != nil {
		return failure("CLOUD_REQUEST_FAILED", "迁移服务请求失败，请检查网络后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failure("CLOUD_HTTP_ERROR", fmt.Sprintf("迁移服务返回 HTTP %d", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return failure("CLOUD_RESPONSE_INVALID", "迁移服务响应无效")
	}
	var envelope struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return failure("CLOUD_RESPONSE_INVALID", "迁移服务响应无效")
	}
	var code string
	if json.Unmarshal(envelope.Code, &code) != nil {
		code = string(envelope.Code)
	}
	if code != "000000" {
		if !codeRE.MatchString(code) {
			code = "unknown"
		}
		return failure("CLOUD_API_ERROR", "迁移服务请求未成功，错误码 "+code)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return failure("CLOUD_RESPONSE_INVALID", "迁移服务数据无效")
	}
	return nil
}
func validateOSSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || !ossHostRE.MatchString(u.Hostname()) {
		return nil, failure("CLOUD_URL_INVALID", "迁移服务返回的 OSS 地址无效")
	}
	return u, nil
}
func (c *Client) Create(ctx context.Context, name string) (*UploadTask, error) {
	if !strings.HasSuffix(name, ".tar.gz") || len(name) > 255 || strings.ContainsAny(name, "/\\\r\n") {
		return nil, failure("CLOUD_FILE_INVALID", "迁移包必须是 tar.gz 文件")
	}
	var task UploadTask
	// Creating a task consumes quota: never automatically retry this operation.
	if err := c.call(ctx, http.MethodPost, "/create", map[string]string{"fileType": "DATA_TRANSFER", "fileName": name}, &task); err != nil {
		return nil, err
	}
	u, err := validateOSSURL(task.Endpoint)
	exp, expErr := time.Parse(time.RFC3339, task.Credentials.Expiration)
	if err != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") || !ValidTaskID(task.TaskID) || !validObjectKey(task.TaskID, task.ObjectKey) || !bucketRE.MatchString(task.Bucket) || !regionRE.MatchString(task.Region) || !endpointRE.MatchString(task.Endpoint) || task.Credentials.AccessKeyID == "" || task.Credentials.AccessKeySecret == "" || task.Credentials.SecurityToken == "" || expErr != nil || !exp.After(time.Now()) {
		return nil, failure("CLOUD_RESPONSE_INVALID", "迁移上传凭据无效或已过期")
	}
	return &task, nil
}
func (c *Client) Complete(ctx context.Context, id, key string) (*Receipt, error) {
	if !ValidTaskID(id) || !validObjectKey(id, key) {
		return nil, failure("INVALID_TASK", "需要有效的 taskId 与 objectKey")
	}
	var receipt Receipt
	if err := c.call(ctx, http.MethodPost, "/complete", map[string]string{"taskId": id, "objectKey": key}, &receipt); err != nil {
		return nil, err
	}
	if receipt.TaskID != id || receipt.ObjectKey != key || receipt.Status != "COMPLETED" || receipt.FileSize <= 0 || int64(receipt.FileSize) > MaxBytes {
		return nil, failure("CLOUD_RESPONSE_INVALID", "迁移上传确认数据无效")
	}
	return &receipt, nil
}
func (c *Client) downloadURL(ctx context.Context, id string) (string, error) {
	if !ValidTaskID(id) {
		return "", failure("INVALID_TASK", "迁移 taskId 无效")
	}
	var result struct {
		TaskID    string `json:"taskId"`
		SignedURL string `json:"signedUrl"`
	}
	if err := c.call(ctx, http.MethodPost, "/download-url", map[string]string{"taskId": id}, &result); err != nil {
		return "", err
	}
	if result.TaskID != id {
		return "", failure("CLOUD_RESPONSE_INVALID", "迁移任务不匹配")
	}
	if _, err := validateOSSURL(result.SignedURL); err != nil {
		return "", err
	}
	return result.SignedURL, nil
}
func (c *Client) Delete(ctx context.Context, id string) error {
	if !ValidTaskID(id) {
		return failure("INVALID_TASK", "迁移 taskId 无效")
	}
	var deleted bool
	if err := c.call(ctx, http.MethodDelete, "/delete?taskId="+url.QueryEscape(id), nil, &deleted); err != nil {
		return err
	}
	if !deleted {
		return failure("CLOUD_DELETE_FAILED", "云端迁移包删除未确认")
	}
	return nil
}

func validObjectKey(id, key string) bool {
	match := objectKeyRE.FindStringSubmatch(key)
	return len(match) == 2 && match[1] == id
}
