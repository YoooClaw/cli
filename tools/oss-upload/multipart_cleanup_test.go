package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

type cleanupSequence struct {
	oss.UploadAPIClient
	initiated          int
	completed, aborted []string
	abortErr           error
}

func (f *cleanupSequence) InitiateMultipartUpload(context.Context, *oss.InitiateMultipartUploadRequest, ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error) {
	f.initiated++
	id := "old"
	if f.initiated > 1 {
		id = "new"
	}
	return &oss.InitiateMultipartUploadResult{UploadId: oss.Ptr(id)}, nil
}
func (f *cleanupSequence) CompleteMultipartUpload(_ context.Context, r *oss.CompleteMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error) {
	id := oss.ToString(r.UploadId)
	f.completed = append(f.completed, id)
	if id == "old" {
		return nil, errors.New("transient failure")
	}
	return &oss.CompleteMultipartUploadResult{}, nil
}
func (f *cleanupSequence) AbortMultipartUpload(_ context.Context, r *oss.AbortMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error) {
	f.aborted = append(f.aborted, oss.ToString(r.UploadId))
	return &oss.AbortMultipartUploadResult{}, f.abortErr
}
func TestCleanupTreatsNoSuchUploadAsSuccess(t *testing.T) {
	fake := &cleanupSequence{abortErr: &oss.ServiceError{StatusCode: 404, Code: "NoSuchUpload"}}
	tracked := &trackedUploadClient{UploadAPIClient: fake, pending: map[string]bool{"old": true}}
	err := tracked.cleanup(&oss.PutObjectRequest{Bucket: oss.Ptr("bucket"), Key: oss.Ptr("key")}, "fixture", func(string, ...any) {})
	if err != nil || len(fake.aborted) != 1 || fake.aborted[0] != "old" {
		t.Fatalf("err=%v aborted=%v", err, fake.aborted)
	}
}
func TestCleanupOnlyAbortsOldIDAfterNewIDCompletes(t *testing.T) {
	fake := &cleanupSequence{}
	tracked := &trackedUploadClient{UploadAPIClient: fake, pending: map[string]bool{}}
	ctx := context.Background()
	for _, wantSuccess := range []bool{false, true} {
		started, err := tracked.InitiateMultipartUpload(ctx, &oss.InitiateMultipartUploadRequest{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = tracked.CompleteMultipartUpload(ctx, &oss.CompleteMultipartUploadRequest{UploadId: started.UploadId})
		if (err == nil) != wantSuccess {
			t.Fatalf("complete error=%v expected success=%v", err, wantSuccess)
		}
	}
	if len(fake.aborted) != 0 {
		t.Fatal("aborted during retries")
	}
	if err := tracked.cleanup(&oss.PutObjectRequest{Bucket: oss.Ptr("bucket"), Key: oss.Ptr("key")}, "fixture", func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if len(fake.aborted) != 1 || fake.aborted[0] != "old" {
		t.Fatalf("aborted=%v; completed new ID must be preserved", fake.aborted)
	}
}
