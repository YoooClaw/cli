package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const SetupAsset = "yoooclaw-setup-win32-x64.exe"
const ReleaseBase = "https://artifact.yoooclaw.com/cli"

var validVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func ValidVersion(version string) bool { return validVersion.MatchString(version) }

func manifestDigest(data []byte, asset string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != 32 || found != "" {
			return "", fmt.Errorf("invalid or duplicate checksum for %s", asset)
		}
		found = strings.ToLower(fields[0])
	}
	if found == "" {
		return "", fmt.Errorf("checksums.txt has no SHA-256 for %s", asset)
	}
	return found, nil
}

// DownloadSetup is fail-closed: a missing/malformed manifest, HTTP failure or
// digest mismatch never falls back to executing an unchecked download.
func DownloadSetup(ctx context.Context, client *http.Client, base, version, dir string) (string, error) {
	if !ValidVersion(version) {
		return "", fmt.Errorf("invalid version")
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid release base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return "", fmt.Errorf("release source requires HTTPS")
	}
	base = strings.TrimRight(base, "/") + "/v" + version + "/"
	get := func(name string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
		}
		if u.Scheme == "https" && resp.Request.URL.Scheme != "https" {
			return nil, fmt.Errorf("insecure download redirect")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("download exceeds size limit")
		}
		return data, nil
	}
	manifest, err := get("checksums.txt", 1<<20)
	if err != nil {
		return "", err
	}
	digest, err := manifestDigest(manifest, SetupAsset)
	if err != nil {
		return "", err
	}
	data, err := get(SetupAsset, 256<<20)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return "", fmt.Errorf("setup SHA-256 mismatch")
	}
	path := filepath.Join(dir, "yoooclaw-setup.exe")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	return path, nil
}
