package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/signer"
)

const cliAbortRuleID = "cli-abort-incomplete-multipart-7d"
const cliAbortRuleXML = "<Rule><ID>" + cliAbortRuleID + "</ID><Prefix>cli/</Prefix><Status>Enabled</Status><AbortMultipartUpload><Days>7</Days></AbortMultipartUpload></Rule>"

func appendCLIAbortRule(raw []byte) ([]byte, error) {
	var config struct {
		XMLName xml.Name
		Rules   []oss.LifecycleRule `xml:"Rule"`
	}
	if err := xml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("invalid lifecycle XML")
	}
	if config.XMLName.Local != "LifecycleConfiguration" {
		return nil, fmt.Errorf("unexpected lifecycle root")
	}
	for _, r := range config.Rules {
		prefix := oss.ToString(r.Prefix)
		if oss.ToString(r.ID) == cliAbortRuleID {
			return nil, fmt.Errorf("rule ID already exists; inspect before changing")
		}
		if r.AbortMultipartUpload != nil && (strings.HasPrefix("cli/", prefix) || strings.HasPrefix(prefix, "cli/")) {
			return nil, fmt.Errorf("overlapping Abort rule found; inspect before changing")
		}
	}
	end := bytes.LastIndex(raw, []byte("</LifecycleConfiguration>"))
	if end < 0 {
		return nil, fmt.Errorf("unsupported lifecycle XML closing tag")
	}
	// Preserve every byte of all pre-existing rules, including unknown SDK fields.
	result := append([]byte{}, raw[:end]...)
	result = append(result, []byte(cliAbortRuleXML)...)
	return append(result, raw[end:]...), nil
}

// Production-only, explicit opt-in maintenance. Normal tests never access OSS.
func TestProductionLifecycleMaintenance(t *testing.T) {
	action := os.Getenv("OSS_LIFECYCLE_ACTION")
	if action == "" {
		t.Skip("production lifecycle maintenance is opt-in")
	}
	if action != "inspect" && action != "apply" {
		t.Fatal("invalid maintenance action")
	}
	backupDir := os.Getenv("OSS_LIFECYCLE_BACKUP_DIR")
	if backupDir == "" {
		t.Fatal("backup directory required")
	}
	for _, name := range []string{"OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET"} {
		if os.Getenv(name) == "" {
			t.Fatalf("missing %s", name)
		}
	}
	client := oss.NewClient(oss.LoadDefaultConfig().WithRegion("cn-hangzhou").WithEndpoint("https://oss-cn-hangzhou.aliyuncs.com").
		WithRetryMaxAttempts(1).WithConnectTimeout(10 * time.Second).WithReadWriteTimeout(30 * time.Second).
		WithCredentialsProvider(credentials.NewEnvironmentVariableCredentialsProvider()))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fail := func(stage string, err error) {
		t.Helper()
		if err != nil {
			var service *oss.ServiceError
			if errors.As(err, &service) {
				t.Fatalf("%s: HTTP %d", stage, service.StatusCode)
			}
			t.Fatalf("%s: request or configuration validation failed", stage)
		}
	}
	get := func() ([]byte, error) {
		input := &oss.OperationInput{OpName: "GetBucketLifecycle", Method: "GET", Bucket: oss.Ptr(defaultBucket), Parameters: map[string]string{"lifecycle": ""}}
		input.OpMetadata.Set(signer.SubResource, []string{"lifecycle"})
		output, err := client.InvokeOperation(ctx, input)
		var service *oss.ServiceError
		if errors.As(err, &service) && service.Code == "NoSuchLifecycle" {
			return []byte("<LifecycleConfiguration></LifecycleConfiguration>"), nil
		}
		if err != nil {
			return nil, err
		}
		defer output.Body.Close()
		return io.ReadAll(io.LimitReader(output.Body, 2*1024*1024))
	}
	before, err := get()
	fail("read current lifecycle", err)
	fail("create backup directory", os.MkdirAll(backupDir, 0700))
	fail("save original XML", os.WriteFile(filepath.Join(backupDir, "before.xml"), before, 0600))
	proposed, err := appendCLIAbortRule(before)
	fail("validate new scoped rule", err)
	fail("save proposed XML", os.WriteFile(filepath.Join(backupDir, "proposed.xml"), proposed, 0600))
	t.Logf("bucket=%s; proposed rule=%s prefix=cli/ AbortMultipartUpload Days=7; original XML preserved", defaultBucket, cliAbortRuleID)
	if action == "inspect" {
		t.Log("inspection only; no bucket configuration changed")
		return
	}
	// Refuse if configuration changed since the backup; do not overwrite another administrator's update.
	latest, err := get()
	fail("re-read before mutation", err)
	if !bytes.Equal(before, latest) {
		t.Fatal("bucket lifecycle changed since backup; refusing write")
	}
	digest := md5.Sum(proposed)
	input := &oss.OperationInput{OpName: "PutBucketLifecycle", Method: "PUT", Bucket: oss.Ptr(defaultBucket), Parameters: map[string]string{"lifecycle": ""},
		Headers: map[string]string{"Content-Type": "application/xml", "Content-MD5": base64.StdEncoding.EncodeToString(digest[:])}, Body: bytes.NewReader(proposed)}
	input.OpMetadata.Set(signer.SubResource, []string{"lifecycle"})
	output, err := client.InvokeOperation(ctx, input)
	fail("apply lifecycle", err)
	if output.Body != nil {
		output.Body.Close()
	}
	after, err := get()
	fail("read back lifecycle", err)
	fail("save readback XML", os.WriteFile(filepath.Join(backupDir, "after.xml"), after, 0600))
	// Compare the complete XML tree, including unknown fields, ignoring formatting and attribute order.
	if !reflect.DeepEqual(lifecycleTokens(t, after), lifecycleTokens(t, proposed)) {
		t.Fatal("lifecycle readback differs from proposed configuration; inspect backup, do not overwrite blindly")
	}
	t.Log("production lifecycle applied and full configuration readback verified")
}

func lifecycleTokens(t *testing.T, raw []byte) []string {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(raw))
	var result []string
	for {
		token, err := d.Token()
		if err == io.EOF {
			return result
		}
		if err != nil {
			t.Fatal("invalid lifecycle XML")
		}
		switch v := token.(type) {
		case xml.StartElement:
			result = append(result, "<"+v.Name.Local+">")
		case xml.EndElement:
			result = append(result, "</"+v.Name.Local+">")
		case xml.CharData:
			if s := strings.TrimSpace(string(v)); s != "" {
				result = append(result, s)
			}
		}
	}
}

func TestAppendCLIAbortRulePreservesUnknownExistingFields(t *testing.T) {
	original := []byte(`<LifecycleConfiguration><Rule><ID>other</ID><Prefix>plugin/</Prefix><Status>Enabled</Status><FutureField>keep</FutureField><Expiration><Days>365</Days></Expiration></Rule></LifecycleConfiguration>`)
	updated, err := appendCLIAbortRule(original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.Replace(updated, []byte(cliAbortRuleXML), nil, 1), original) {
		t.Fatal("existing XML changed")
	}
	for _, prefix := range []string{"", "cli/", "cli/runs/"} {
		raw := []byte(`<LifecycleConfiguration><Rule><Prefix>` + prefix + `</Prefix><AbortMultipartUpload><Days>1</Days></AbortMultipartUpload></Rule></LifecycleConfiguration>`)
		if _, err := appendCLIAbortRule(raw); err == nil {
			t.Fatalf("accepted overlapping prefix %q", prefix)
		}
	}
}
