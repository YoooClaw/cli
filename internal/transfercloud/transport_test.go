package transfercloud

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Keep production URL validation while directing test OSS requests to a local
// server. The signed URL and real API key are never needed for these tests.
func mockOSSClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("cli-key", "api.example")
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = "http"
		u.Host = strings.TrimPrefix(srv.URL, "http://")
		clone.URL = &u
		return http.DefaultTransport.RoundTrip(clone)
	})
	return c
}
func TestSDKDownloadResumesInterruptedRange(t *testing.T) {
	payload := bytes.Repeat([]byte("migration-data-"), 1300000)
	var mu sync.Mutex
	interrupted, resumed := false, false
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/download-url" {
			reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL})
			return
		}
		if r.Method != "GET" {
			t.Error("signed GET URL used with another method")
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("X-Api-Key-Id") != "" {
			t.Error("API key forwarded to OSS")
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error("missing range")
			w.WriteHeader(400)
			return
		}
		if start > partSize && start < 2*partSize {
			mu.Lock()
			resumed = true
			mu.Unlock()
		}
		w.Header().Set("ETag", `"stable-version"`)
		w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(206)
		mu.Lock()
		breakNow := start == partSize && !interrupted
		if breakNow {
			interrupted = true
		}
		mu.Unlock()
		if breakNow {
			w.Write(payload[start : start+300000])
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Write(payload[start : end+1])
	})
	path := filepath.Join(t.TempDir(), "package.tar.gz")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Download(ctx, testID, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("download bytes differ")
	}
	mu.Lock()
	defer mu.Unlock()
	if !interrupted || !resumed {
		t.Fatalf("interrupted=%v resumed=%v", interrupted, resumed)
	}
}
func TestDownloadRejectsOversizeAndWrongRange(t *testing.T) {
	for _, scenario := range []string{"oversize", "wrong-range", "changed-etag", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario == "redirect" {
					http.Redirect(w, r, "https://evil.example/leak", 302)
					return
				}
				total := int64(10)
				rng := "bytes 0-0/10"
				etag := `"stable"`
				if scenario == "oversize" {
					total = MaxBytes + 1
					rng = fmt.Sprintf("bytes 0-0/%d", total)
				}
				if r.Header.Get("Range") != "bytes=0-0" {
					rng = "bytes 0-9/10"
					if scenario == "wrong-range" {
						rng = "bytes 1-9/10"
					}
					if scenario == "changed-etag" {
						etag = `"changed"`
					}
				}
				w.Header().Set("Content-Range", rng)
				w.Header().Set("ETag", etag)
				if r.Header.Get("Range") == "bytes=0-0" {
					w.Header().Set("Content-Length", "1")
				} else {
					w.Header().Set("Content-Length", "10")
				}
				w.WriteHeader(206)
				if r.Header.Get("Range") == "bytes=0-0" {
					w.Write([]byte("x"))
				} else {
					w.Write([]byte("0123456789"))
				}
			})
			d := &signedDownload{client: c.http, url: signedURL}
			_, err := d.HeadObject(context.Background(), &oss.HeadObjectRequest{})
			if scenario == "oversize" || scenario == "redirect" {
				if err == nil {
					t.Fatal("accepted invalid metadata")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result, err := d.GetObject(context.Background(), &oss.GetObjectRequest{Range: oss.Ptr("bytes=0-9")}); err == nil {
				result.Body.Close()
				t.Fatal("accepted wrong range/version")
			}
		})
	}
}
func TestUploadRetriesConfirmationWithoutReupload(t *testing.T) {
	var creates, puts, completes int
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/create":
			creates++
			reply(w, testTask())
		case apiPath + "/complete":
			completes++
			if completes == 1 {
				w.WriteHeader(503)
				return
			}
			reply(w, map[string]any{"taskId": testID, "objectKey": testKey, "status": "COMPLETED", "fileSize": "7"})
		default:
			if r.Method != "PUT" {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				w.WriteHeader(400)
				return
			}
			puts++
			b, _ := io.ReadAll(r.Body)
			if string(b) != "archive" {
				t.Error("upload data changed")
			}
			crc := oss.NewCRC64(0)
			crc.Write(b)
			w.Header().Set("x-oss-hash-crc64ecma", fmt.Sprint(crc.Sum64()))
			w.Header().Set("ETag", `"test"`)
			w.WriteHeader(200)
		}
	})
	file := filepath.Join(t.TempDir(), "migration.tar.gz")
	os.WriteFile(file, []byte("archive"), 0600)
	receipt, err := c.Upload(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TaskID != testID || creates != 1 || puts != 1 || completes != 2 {
		t.Fatalf("create=%d put=%d complete=%d", creates, puts, completes)
	}
}

func TestSDKMultipartUploadResumesCheckpoint(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), int(2*partSize+123))
	var mu sync.Mutex
	calls := map[int]int{}
	parts := map[int][]byte{}
	creates, inits, lists := 0, 0, 0
	retryAllowed, completed := false, false
	firstTwo := make(chan struct{})
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/create" {
			mu.Lock()
			creates++
			mu.Unlock()
			reply(w, testTask())
			return
		}
		if r.URL.Path == apiPath+"/complete" {
			mu.Lock()
			done := completed
			retryAllowed = true
			mu.Unlock()
			if !done {
				w.WriteHeader(409)
				return
			}
			reply(w, map[string]any{"taskId": testID, "objectKey": testKey, "status": "COMPLETED", "fileSize": len(payload)})
			return
		}
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		switch r.Method {
		case "POST":
			if q.Get("uploadId") == "" {
				mu.Lock()
				inits++
				mu.Unlock()
				fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>`+testKey+`</Key><UploadId>checkpoint-upload</UploadId></InitiateMultipartUploadResult>`)
				return
			}
			mu.Lock()
			got := append(append(append([]byte{}, parts[1]...), parts[2]...), parts[3]...)
			completed = true
			mu.Unlock()
			if !bytes.Equal(got, payload) {
				t.Error("multipart content differs")
			}
			hash := oss.NewCRC64(0)
			hash.Write(got)
			w.Header().Set("x-oss-hash-crc64ecma", fmt.Sprint(hash.Sum64()))
			fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>bucket</Bucket><Key>`+testKey+`</Key><ETag>complete</ETag></CompleteMultipartUploadResult>`)
		case "PUT":
			n, _ := strconv.Atoi(q.Get("partNumber"))
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			calls[n]++
			failNow := n == 3 && !retryAllowed
			mu.Unlock()
			if failNow {
				select {
				case <-firstTwo:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(403)
				fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>test interruption</Message></Error>`)
				return
			}
			mu.Lock()
			parts[n] = data
			if n <= 2 && len(parts[1]) > 0 && len(parts[2]) > 0 {
				select {
				case <-firstTwo:
				default:
					close(firstTwo)
				}
			}
			mu.Unlock()
			hash := oss.NewCRC64(0)
			hash.Write(data)
			w.Header().Set("x-oss-hash-crc64ecma", fmt.Sprint(hash.Sum64()))
			w.Header().Set("ETag", fmt.Sprintf("part%d", n))
			w.WriteHeader(200)
		case "GET":
			mu.Lock()
			defer mu.Unlock()
			lists++
			fmt.Fprint(w, `<ListPartsResult><Bucket>bucket</Bucket><Key>`+testKey+`</Key><UploadId>checkpoint-upload</UploadId><IsTruncated>false</IsTruncated>`)
			for n := 1; n <= 3; n++ {
				if data := parts[n]; data != nil {
					hash := oss.NewCRC64(0)
					hash.Write(data)
					fmt.Fprintf(w, "<Part><PartNumber>%d</PartNumber><Size>%d</Size><ETag>part%d</ETag><HashCrc64ecma>%d</HashCrc64ecma></Part>", n, len(data), n, hash.Sum64())
				}
			}
			fmt.Fprint(w, `</ListPartsResult>`)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(400)
		}
	})
	file := filepath.Join(t.TempDir(), "migration.tar.gz")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := c.Upload(ctx, file); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 1 || inits != 1 || lists != 1 || calls[1] != 1 || calls[2] != 1 || calls[3] != 2 {
		t.Fatalf("create=%d init=%d list=%d parts=%v", creates, inits, lists, calls)
	}
}
