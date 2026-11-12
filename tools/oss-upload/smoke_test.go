package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

// Explicit opt-in only: never runs against OSS in normal CI or local tests.
func TestOSSUploadSmoke(t *testing.T) {
	if os.Getenv("OSS_UPLOAD_SMOKE_TEST") != "1" {
		t.Skip("real OSS probe is opt-in")
	}
	bucket := os.Getenv("OSS_BUCKET")
	if bucket != "yoooclaw-artifacts-test" && bucket != defaultBucket {
		t.Fatal("unexpected smoke test bucket")
	}
	for _, name := range []string{"OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET"} {
		if os.Getenv(name) == "" {
			t.Fatalf("missing %s", name)
		}
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal("random prefix generation failed")
	}
	prefix := fmt.Sprintf("cli/runs/oss-upload-smoke/%x", suffix)
	client := oss.NewClient(oss.LoadDefaultConfig().WithRegion("cn-hangzhou").
		WithEndpoint("https://oss-accelerate.aliyuncs.com").WithRetryMaxAttempts(1).
		WithConnectTimeout(10 * time.Second).WithReadWriteTimeout(30 * time.Second).
		WithCredentialsProvider(credentials.NewEnvironmentVariableCredentialsProvider()))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	check := func(stage string, err error) {
		t.Helper()
		if err != nil {
			reason := "network or SDK error"
			var service *oss.ServiceError
			if errors.As(err, &service) {
				reason = fmt.Sprintf("HTTP %d", service.StatusCode)
			}
			t.Fatalf("%s failed: %s", stage, reason)
		}
	}
	t.Logf("isolated accelerated probe: %s/%s", bucket, prefix)
	var keys []string
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		for _, key := range keys {
			if _, err := client.DeleteObject(cleanupCtx, &oss.DeleteObjectRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key)}); err != nil {
				t.Errorf("temporary object cleanup failed: %s", key)
			}
		}
	})
	u := &uploader{client: client, bucket: bucket, cacheControl: "no-cache"}
	for _, size := range []int{128, 2*uploadPartSize + 100} {
		body := make([]byte, size)
		_, err := rand.Read(body)
		check("fixture generation", err)
		key := fmt.Sprintf("%s/%d.bin", prefix, size)
		keys = append(keys, key)
		file := ""
		if size > uploadPartSize {
			file = filepath.Join(t.TempDir(), "multipart.bin")
			check("fixture write", os.WriteFile(file, body, 0600))
		}
		check("accelerated upload", u.uploadContext(ctx, key, body, file, "application/octet-stream", fmt.Sprint(size)))
		got, err := client.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key)})
		check("readback", err)
		data, readErr := io.ReadAll(got.Body)
		got.Body.Close()
		check("readback body", readErr)
		if !bytes.Equal(data, body) {
			t.Fatal("uploaded bytes differ")
		}
		t.Logf("%d-byte PUT/multipart completed and bytes verified", size)
	}
	// Verify Abort permission on an actual unfinished upload, using production cleanup code.
	key := prefix + "/aborted.bin"
	tracked := &trackedUploadClient{UploadAPIClient: client, pending: map[string]bool{}}
	started, err := tracked.InitiateMultipartUpload(ctx, &oss.InitiateMultipartUploadRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key)})
	check("multipart initiation for Abort", err)
	// Best-effort retry on test failure, without using an already-cancelled request context.
	aborted := false
	t.Cleanup(func() {
		if aborted {
			return
		}
		if err := tracked.cleanup(&oss.PutObjectRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key)}, "smoke Abort cleanup", t.Logf); err != nil {
			t.Error(err)
		}
	})
	_, err = client.UploadPart(ctx, &oss.UploadPartRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key), UploadId: started.UploadId, PartNumber: 1, Body: bytes.NewReader(make([]byte, uploadPartSize))})
	check("part upload before Abort", err)
	check("AbortMultipartUpload permission", tracked.cleanup(&oss.PutObjectRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key)}, "smoke Abort", t.Logf))
	aborted = true
	_, err = client.ListParts(ctx, &oss.ListPartsRequest{Bucket: oss.Ptr(bucket), Key: oss.Ptr(key), UploadId: started.UploadId})
	var service *oss.ServiceError
	if !errors.As(err, &service) || service.Code != "NoSuchUpload" {
		t.Fatal("could not confirm aborted multipart upload is absent")
	}
	t.Log("AbortMultipartUpload succeeded; NoSuchUpload confirmed")
	lifecycle, err := client.GetBucketLifecycle(ctx, &oss.GetBucketLifecycleRequest{Bucket: oss.Ptr(bucket)})
	if err != nil {
		t.Log("bucket lifecycle configuration remains unconfirmed (read unavailable)")
		return
	}
	count := 0
	if lifecycle.LifecycleConfiguration != nil {
		for _, rule := range lifecycle.LifecycleConfiguration.Rules {
			if oss.ToString(rule.Status) == "Enabled" && rule.AbortMultipartUpload != nil && strings.HasPrefix("cli/", oss.ToString(rule.Prefix)) && len(rule.Tags) == 0 && rule.Filter == nil {
				count++
			}
		}
	}
	t.Logf("enabled unfiltered AbortMultipartUpload rules covering cli/: %d", count)
}
