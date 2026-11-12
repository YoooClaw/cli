package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeUpload struct {
	oss.UploadAPIClient
	mu                   sync.Mutex
	parts                map[int32]int
	sizes                map[int32]int64
	init, complete, puts int
	failComplete         bool
	putErr               error
	completeErr          error
	cancelComplete       context.CancelFunc
	abortErr             error
	aborts               int
	abortContextErr      error
	abortDeadline        bool
	contentType, cache   string
	acl                  oss.ObjectACLType
}

func (f *fakeUpload) InitiateMultipartUpload(_ context.Context, r *oss.InitiateMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error) {
	f.init++
	f.contentType = oss.ToString(r.ContentType)
	f.cache = oss.ToString(r.CacheControl)
	return &oss.InitiateMultipartUploadResult{UploadId: oss.Ptr("same-upload")}, nil
}
func (f *fakeUpload) UploadPart(_ context.Context, r *oss.UploadPartRequest, _ ...func(*oss.Options)) (*oss.UploadPartResult, error) {
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parts[r.PartNumber]++
	f.sizes[r.PartNumber] = n
	return &oss.UploadPartResult{ETag: oss.Ptr(fmt.Sprint(r.PartNumber))}, nil
}
func (f *fakeUpload) CompleteMultipartUpload(_ context.Context, r *oss.CompleteMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error) {
	f.acl = r.Acl
	f.complete++
	if f.cancelComplete != nil {
		f.cancelComplete()
		return nil, context.Canceled
	}
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	if f.failComplete && f.complete == 1 {
		return nil, errors.New("SECRET request timeout")
	}
	return &oss.CompleteMultipartUploadResult{}, nil
}
func (f *fakeUpload) ListParts(_ context.Context, r *oss.ListPartsRequest, _ ...func(*oss.Options)) (*oss.ListPartsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := []oss.Part{}
	for n, size := range f.sizes {
		p = append(p, oss.Part{PartNumber: n, Size: size, ETag: oss.Ptr(fmt.Sprint(n))})
	}
	sort.Slice(p, func(i, j int) bool { return p[i].PartNumber < p[j].PartNumber })
	return &oss.ListPartsResult{Parts: p}, nil
}
func (f *fakeUpload) PutObject(_ context.Context, r *oss.PutObjectRequest, _ ...func(*oss.Options)) (*oss.PutObjectResult, error) {
	f.puts++
	return &oss.PutObjectResult{}, f.putErr
}

func TestSDKMultipartResumesAndLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, make([]byte, 2*uploadPartSize+100), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeUpload{parts: map[int32]int{}, sizes: map[int32]int64{}, failComplete: true}
	var mu sync.Mutex
	var logs []string
	log := func(s string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(s, args...))
	}
	req := &oss.PutObjectRequest{Bucket: oss.Ptr("bucket"), Key: oss.Ptr("key"), ContentType: oss.Ptr("application/octet-stream"), CacheControl: oss.Ptr("no-cache"), Acl: oss.ObjectACLPrivate}
	if err := uploadWithProgress(context.Background(), f, req, nil, path, "binary", log); err != nil {
		t.Fatal(err)
	}
	if f.init != 1 || f.complete != 2 || f.parts[1] != 1 || f.parts[2] != 1 || f.puts != 0 || f.aborts != 0 {
		t.Fatalf("SDK did not resume: %+v", f)
	}
	if f.contentType != "application/octet-stream" || f.cache != "no-cache" || f.acl != oss.ObjectACLPrivate {
		t.Fatal("metadata lost")
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"SDK 分片", "checkpoint", "100%", "上传完成", "耗时"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(joined, "SECRET") {
		t.Fatal("raw error leaked")
	}
}
func TestUploadPermissionFailureDoesNotRetry(t *testing.T) {
	f := &fakeUpload{putErr: &oss.ServiceError{StatusCode: 403, Code: "AccessDenied"}}
	err := uploadWithProgress(context.Background(), f, &oss.PutObjectRequest{Bucket: oss.Ptr("bucket"), Key: oss.Ptr("key")}, []byte("small"), "", "marker", func(string, ...any) {})
	if err == nil || f.puts != 1 {
		t.Fatalf("err=%v calls=%d", err, f.puts)
	}
}
func TestUploadRetryLimitAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			f := &fakeUpload{putErr: errors.New("network")}
			ctx := context.Background()
			if cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			err := uploadWithProgress(ctx, f, &oss.PutObjectRequest{Bucket: oss.Ptr("bucket"), Key: oss.Ptr("key")}, []byte("small"), "", "marker", func(string, ...any) {})
			expected := 3
			if cancelled {
				expected = 1
			}
			if err == nil || f.puts != expected {
				t.Fatalf("err=%v calls=%d", err, f.puts)
			}
		})
	}
}

func TestUploadContextPreservesParentCancellation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("ETag", "fixture")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := oss.LoadDefaultConfig().WithRegion("cn-hangzhou").WithEndpoint(server.URL).
		WithUsePathStyle(true).WithRetryMaxAttempts(1).
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test-key", "test-secret"))
	u := &uploader{client: oss.NewClient(cfg), bucket: "test-bucket"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := u.uploadContext(ctx, "key", []byte("fixture"), "", "text/plain", "fixture")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("sent %d requests after cancellation", requests.Load())
	}
}

func (f *fakeUpload) AbortMultipartUpload(ctx context.Context, _ *oss.AbortMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error) {
	f.aborts++
	f.abortContextErr = ctx.Err()
	_, f.abortDeadline = ctx.Deadline()
	return &oss.AbortMultipartUploadResult{}, f.abortErr
}

func TestMultipartCleanupAfterFinalFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		cancel, abortFails bool
	}{
		{name: "permission failure"}, {name: "peer cancellation", cancel: true}, {name: "abort denied", abortFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(path, make([]byte, 2*uploadPartSize+100), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeUpload{parts: map[int32]int{}, sizes: map[int32]int64{}, completeErr: &oss.ServiceError{StatusCode: 403, Code: "AccessDenied"}}
			if tc.cancel {
				f.cancelComplete = cancel
			}
			if tc.abortFails {
				f.abortErr = &oss.ServiceError{StatusCode: 403, Code: "AccessDenied"}
			}
			var mu sync.Mutex
			var logs []string
			err := uploadWithProgress(ctx, f, &oss.PutObjectRequest{Bucket: oss.Ptr("bucket"), Key: oss.Ptr("key")}, nil, path, "binary", func(format string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				logs = append(logs, fmt.Sprintf(format, args...))
			})
			if err == nil || f.aborts != 1 {
				t.Fatalf("error=%v aborts=%d", err, f.aborts)
			}
			if f.abortContextErr != nil || !f.abortDeadline {
				t.Fatalf("cleanup context: error=%v deadline=%v", f.abortContextErr, f.abortDeadline)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if tc.abortFails && !strings.Contains(err.Error(), "分片清理失败（HTTP 403）") {
				t.Fatalf("lost cleanup failure: %v", err)
			}
			if !tc.abortFails && !strings.Contains(strings.Join(logs, "\n"), "未完成分片已清理") {
				t.Fatal("missing cleanup log")
			}
		})
	}
}
