package transfercloud

import (
	"errors"
	"github.com/YoooClaw/cli/internal/errs"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticDescriptionRedaction(t *testing.T) {
	input := "Object not committed.\n" +
		`{"accessKeyId":"id-secret","accessKeySecret":"key-secret","securityToken":"sts-secret","signedUrl":"https://bucket.test/a?Signature=url-secret","authorization":"Bearer auth-secret"} ` +
		"<SecurityToken>xml-secret</SecurityToken> Signature=query-secret Bearer bare-secret " +
		"known+secret known%2Bsecret"
	got := redactDiagnostic(input, "known+secret")
	if !strings.Contains(got, "Object not committed.") {
		t.Fatal(got)
	}
	for _, secret := range []string{"id-secret", "key-secret", "sts-secret", "url-secret", "auth-secret", "xml-secret", "query-secret", "bare-secret", "known+secret", "known%2Bsecret", "\n"} {
		if strings.Contains(got, secret) {
			t.Fatalf("leaked %q: %s", secret, got)
		}
	}
	if len([]rune(got)) > maxHTTPErrorRunes+1 {
		t.Fatal("unbounded message")
	}
}
func TestDiagnosticRedactsBeforeTruncation(t *testing.T) {
	secret := strings.Repeat("private", 80)
	got := redactDiagnostic(strings.Repeat("a", 290)+" "+secret, secret)
	if strings.Contains(got, "private") {
		t.Fatal(got)
	}
	got = redactDiagnostic(`{"token":"` + secret)
	if strings.Contains(got, "private") {
		t.Fatal(got)
	}
}
func TestProviderDiagnosticRedactsOldAndCurrentCredentials(t *testing.T) {
	p := &uploadCredentials{cloud: New("api-secret", "example.test"), previousSecrets: []string{"old-secret"}}
	p.task.Credentials.SecurityToken = "current-secret"
	e := &HTTPStatusError{StatusCode: 409, Err: errs.New("CLOUD_HTTP_ERROR", "unchanged"), Detail: "Object missing; api-secret old-secret current-secret"}
	detail := p.failureDetail("complete", 1, time.Now(), e)
	if !strings.Contains(detail, "Object missing") || strings.Contains(detail, "-secret") {
		t.Fatal(detail)
	}
	if !errors.Is(withUploadDiagnostics(e, []string{detail}), e) {
		t.Fatal("public chain changed")
	}
}

func TestDiagnosticPreservesOSSProseAndRedactsObfuscatedSecrets(t *testing.T) {
	for _, text := range []string{"The OSS Access Key Id you provided does not exist", "OSS request failed:", "Basic request failed:"} {
		if got := redactDiagnostic(text); got != text {
			t.Fatalf("got %q want %q", got, text)
		}
	}
	for _, text := range []string{"pri\u200bvate-secret", "pri\x00vate-secret", "private-secret", "pri&#8203;vate-secret"} {
		if got := redactDiagnostic(text, "private-secret"); got != "[redacted]" {
			t.Fatalf("secret leaked: %q", got)
		}
	}
	for _, text := range []string{"Basic dXNlcjpwYXNz", "OSS accessid:c2lnbmF0dXJl=", "OSS4-HMAC-SHA256 credentials"} {
		if got := redactDiagnostic(text); !strings.Contains(got, "[redacted]") {
			t.Fatalf("auth not redacted: %q", got)
		}
	}
}
