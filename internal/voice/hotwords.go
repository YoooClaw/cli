package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

type HotwordClient struct {
	Host   string
	APIKey string
}

type Hotword struct {
	Text   string `json:"text"`
	Weight int    `json:"weight"`
}

type HotwordPage struct {
	Items      []Hotword `json:"items"`
	NextCursor *int64    `json:"next_cursor,omitempty"`
}

func (c HotwordClient) List(ctx context.Context, limit int, cursor int64) (HotwordPage, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor > 0 {
		query.Set("cursor", strconv.FormatInt(cursor, 10))
	}
	resp, err := c.request(ctx, http.MethodGet, "?"+query.Encode(), nil, http.StatusOK)
	if err != nil {
		return HotwordPage{}, err
	}
	defer resp.Body.Close()
	var page HotwordPage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&page); err != nil || page.Items == nil {
		return HotwordPage{}, errs.New(errs.CodeNetworkError, "热词列表响应格式异常，缺少有效的 items 数组")
	}
	return page, nil
}

// Mutate sends one word. Janus acknowledges both changes and idempotent no-ops with 204.
func (c HotwordClient) Mutate(ctx context.Context, action, text string, weight int) *errs.Error {
	body, err := json.Marshal(struct {
		Action string `json:"action"`
		Text   string `json:"text"`
		Weight int    `json:"weight,omitempty"`
	}{action, text, weight})
	if err != nil {
		return errs.New(errs.CodeInvalidArgument, err.Error())
	}
	resp, requestErr := c.request(ctx, http.MethodPost, "", bytes.NewReader(body), http.StatusNoContent)
	if requestErr != nil {
		if action == "update" && requestErr.Details["http_status"] == http.StatusNotFound {
			requestErr.Code = errs.CodeNotFound
		}
		return requestErr
	}
	resp.Body.Close()
	return nil
}

func (c HotwordClient) request(ctx context.Context, method, query string, body io.Reader, want int) (*http.Response, *errs.Error) {
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.APIKey), "Bearer "))
	if key == "" {
		return nil, errs.New(errs.CodeCredentialMissing, "请先用 yoooclaw auth set-api-key - 配置默认 API Key")
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.Host+"/unauth/janus/asr/v1/words"+query, body)
	if err != nil {
		return nil, errs.New(errs.CodeConfigInvalid, "热词服务地址无效，请检查 yoooclaw env show")
	}
	req.Header.Set("X-Api-Key-Id", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "未收到热词服务响应，请检查网络。写入结果可用 list 核对")
	}
	if resp.StatusCode == want {
		return resp, nil
	}
	defer resp.Body.Close()
	code := errs.CodeNetworkError
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = errs.CodeUnauthorized
	case http.StatusConflict:
		code = errs.CodeAlreadyExists
	case http.StatusUnprocessableEntity:
		code = errs.CodeInvalidArgument
	}
	var envelope struct {
		Detail string `json:"detail"`
	}
	message := fmt.Sprintf("热词服务返回 HTTP %d", resp.StatusCode)
	if json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&envelope) == nil && envelope.Detail != "" {
		message += "：" + strings.ReplaceAll(envelope.Detail, key, "[redacted]")
	}
	failure := errs.New(code, message, map[string]any{"http_status": resp.StatusCode})
	if code == errs.CodeUnauthorized {
		failure.WithHint("用 yoooclaw env show 查看环境，用 yoooclaw auth status 核对默认 Key")
	}
	return nil, failure
}
