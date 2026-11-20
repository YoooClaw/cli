package transfercloud

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	diagnosticURLRE       = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	diagnosticAuthRE      = regexp.MustCompile(`(?i)\b(Bearer|OSS4-HMAC-SHA256)\s+[^\s,;"<>]+`)
	diagnosticBasicRE     = regexp.MustCompile(`(?i)\b(Basic)\s+[A-Za-z0-9+/]+={0,2}`)
	diagnosticLegacyOSSRE = regexp.MustCompile(`\b(OSS)\s+[A-Za-z0-9]+:[A-Za-z0-9+/=]+`)
	diagnosticSecretRE    = regexp.MustCompile(`(?i)(["']?(?:access[_-]?key(?:[_-]?(?:id|secret))?|secret(?:accesskey)?|security[_-]?token|x-oss-security-token|x-api-key-id|api[_-]?key|authorization|signature|credential|password|token|signed[_-]?url)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*(?:"|$)|'(?:\\.|[^'\\])*(?:'|$)|[^\s,;}<]+)`)
	diagnosticXMLSecretRE = regexp.MustCompile(`(?is)(<(?:AccessKeyId|AccessKeySecret|SecurityToken|Signature|Authorization|Password|Token|SignedUrl)>)[^<]*`)
)

// Sanitize before truncating so a secret is never exposed as a truncated prefix.
// Only descriptive text is accepted; callers must not pass SDK snapshots/headers.
func redactDiagnostic(text string, secrets ...string) string {
	text = stripDiagnosticFormat(html.UnescapeString(strings.ToValidUTF8(text, "")))
	for _, secret := range secrets {
		secret = stripDiagnosticFormat(secret)
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), string(encoded[1 : len(encoded)-1])} {
			text = strings.ReplaceAll(text, form, "[redacted]")
		}
	}
	text = diagnosticURLRE.ReplaceAllString(text, "[redacted-url]")
	text = diagnosticAuthRE.ReplaceAllString(text, "$1 [redacted]")
	text = diagnosticBasicRE.ReplaceAllStringFunc(text, func(value string) string {
		parts := strings.Fields(value)
		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(parts[1])
		}
		if err == nil && strings.Contains(string(decoded), ":") {
			return parts[0] + " [redacted]"
		}
		return value
	})
	text = diagnosticLegacyOSSRE.ReplaceAllString(text, "$1 [redacted]")
	text = diagnosticSecretRE.ReplaceAllString(text, "${1}[redacted]")
	text = diagnosticXMLSecretRE.ReplaceAllString(text, "${1}[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > maxHTTPErrorRunes {
		text = string(runes[:maxHTTPErrorRunes]) + "…"
	}
	return text
}

func stripDiagnosticFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && !unicode.IsSpace(r)) {
			return -1
		}
		return r
	}, s)
}
