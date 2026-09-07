// oss-upload 把 CLI 发布制品和安装脚本上传到阿里云 OSS（参考 hermes-plugin 的布局）。
//
// 用法（在仓库任意目录下）：
//
//	go -C tools/oss-upload run . [--version 0.6.0] [--only install|artifacts] [--dry-run]
//
// 环境变量（可放 .env，进程环境优先）：
//
//	OSS_REGION            如 oss-cn-hangzhou（SDK v2 需要的 cn-hangzhou 形式会自动归一化）
//	OSS_ACCESS_KEY_ID     必填（--dry-run 除外）
//	OSS_ACCESS_KEY_SECRET 必填（--dry-run 除外）
//	OSS_BUCKET            默认 yoooclaw-artifacts
//	OSS_PUBLIC_URL        默认 https://artifact.yoooclaw.com
//	OSS_PREFIX            默认 cli
//	OSS_ENDPOINT          可选 endpoint 覆盖
//	OSS_OBJECT_ACL        可选对象 ACL
//	OSS_CACHE_CONTROL     默认 no-cache, no-store, max-age=0, must-revalidate
//	DIST_DIR              默认 <repo>/dist-native
//
// OSS 布局（与 hermes-plugin 一致的组织方式）：
//
//	<prefix>/install.sh                         渲染后的 Unix 安装脚本（live，始终指向 OSS）
//	<prefix>/install.ps1                        渲染后的 Windows 安装脚本（live）
//	<prefix>/install-wuying.sh                  渲染后的无影一键安装脚本（live）
//	<prefix>/v<ver>/yoooclaw-<os>-<arch>        原生二进制
//	<prefix>/v<ver>/checksums.txt               sha256 清单
//	<prefix>/v<ver>/installer/install.*         通用安装脚本归档
//	<prefix>/v<ver>/installer/install-wuying.sh 无影安装脚本归档
//	<prefix>/v<ver>/oss-manifest.json       本次发布清单
//	<prefix>/latest                         正式版版本标记（纯版本号文本）
//
// 预发布版本只写 <prefix>/v<ver>/** 归档，不碰任何 live key、也不写渠道标记：
// 默认安装路径永远解析到最新正式版，装 beta 必须显式指定版本号。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

const (
	sentinelBaseURL        = "__YOOOCLAW_CLI_OSS_BASE_URL__"
	sentinelRendered       = "__YOOOCLAW_CLI_TEMPLATE_RENDERED__"
	sentinelReleaseVersion = "__YOOOCLAW_CLI_RELEASE_VERSION__"
	rawInstallerURL        = "https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh"
	rawWuyingInstallerURL  = "https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install-wuying.sh"
	rawInstallerPS1URL     = "https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.ps1"

	defaultBucket       = "yoooclaw-artifacts"
	defaultPublicURL    = "https://artifact.yoooclaw.com"
	defaultPrefix       = "cli"
	defaultCacheControl = "no-cache, no-store, max-age=0, must-revalidate"

	putTimeout  = 2 * time.Minute
	maxAttempts = 3
)

// dist-native 里的完整制品集合；缺一即失败，防止上传残缺版本。
var nativeArtifacts = []string{
	"yoooclaw-darwin-arm64",
	"yoooclaw-darwin-x64",
	"yoooclaw-linux-arm64",
	"yoooclaw-linux-x64",
	"yoooclaw-win32-x64.exe",
	"checksums.txt",
}

func logf(format string, args ...any) { fmt.Printf("\x1b[36m[upload]\x1b[0m "+format+"\n", args...) }
func okf(format string, args ...any)  { fmt.Printf("\x1b[32m[upload]\x1b[0m "+format+"\n", args...) }
func warnf(format string, args ...any) {
	fmt.Printf("\x1b[33m[upload] WARN:\x1b[0m "+format+"\n", args...)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\x1b[31m[upload] ERROR:\x1b[0m "+format+"\n", args...)
	os.Exit(1)
}

func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fatalf("未找到仓库根目录（沿 %s 向上找不到 package.json）", dir)
		}
		dir = parent
	}
}

// loadDotEnv 读取 <root>/.env，仅填充进程环境里不存在的变量。
func loadDotEnv(root string) {
	data, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, strings.TrimSpace(value))
		}
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		fatalf("缺少环境变量: %s", key)
	}
	return v
}

func packageVersion(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		fatalf("读取 package.json: %v", err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		fatalf("解析 package.json: %v", err)
	}
	if pkg.Version == "" {
		fatalf("package.json 缺少 version")
	}
	return pkg.Version
}

func joinKey(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, "/")
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return strings.Join(cleaned, "/")
}

func urlForKey(publicURL, key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.TrimRight(publicURL, "/") + "/" + strings.Join(segments, "/")
}

func contentTypeFor(name string) string {
	switch {
	case name == "checksums.txt" || strings.HasSuffix(name, ".txt"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".sh"):
		return "text/x-shellscript; charset=utf-8"
	case strings.HasSuffix(name, ".ps1"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

type artifact struct {
	Filename    string `json:"filename"`
	Key         string `json:"key"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`

	localPath string
}

type uploader struct {
	client       *oss.Client
	bucket       string
	publicURL    string
	acl          string
	cacheControl string
	dryRun       bool
}

func (u *uploader) put(key string, body []byte, contentType, label string) string {
	publicURL := urlForKey(u.publicURL, key)
	if u.dryRun {
		logf("would upload %s -> %s", label, key)
		return publicURL
	}
	logf("uploading %s -> %s", label, key)

	req := &oss.PutObjectRequest{
		Bucket:       oss.Ptr(u.bucket),
		Key:          oss.Ptr(key),
		Body:         bytes.NewReader(body),
		ContentType:  oss.Ptr(contentType),
		CacheControl: oss.Ptr(u.cacheControl),
	}
	if u.acl != "" {
		req.Acl = oss.ObjectACLType(u.acl)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), putTimeout)
		_, err := u.client.PutObject(ctx, req)
		cancel()
		if err == nil {
			okf("done: %s", publicURL)
			return publicURL
		}
		lastErr = err
		if attempt < maxAttempts {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			warnf("upload %s 失败（第 %d/%d 次）: %v，%s 后重试", label, attempt, maxAttempts, err, backoff)
			time.Sleep(backoff)
			if _, err := req.Body.(*bytes.Reader).Seek(0, 0); err != nil {
				fatalf("重置上传流失败: %v", err)
			}
		}
	}
	fatalf("upload %s 连续 %d 次失败: %v", label, maxAttempts, lastErr)
	return ""
}

func collectArtifacts(distDir, prefix, publicURL, version string) []artifact {
	artifacts := make([]artifact, 0, len(nativeArtifacts))
	var missing []string
	for _, name := range nativeArtifacts {
		path := filepath.Join(distDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		sum := sha256.Sum256(data)
		key := joinKey(prefix, "v"+version, name)
		artifacts = append(artifacts, artifact{
			Filename:    name,
			Key:         key,
			URL:         urlForKey(publicURL, key),
			SHA256:      hex.EncodeToString(sum[:]),
			Size:        int64(len(data)),
			ContentType: contentTypeFor(name),
			localPath:   path,
		})
	}
	if len(missing) > 0 {
		fatalf("%s 缺少制品: %s（先跑 scripts/build-go.sh 并生成 checksums.txt）", distDir, strings.Join(missing, ", "))
	}

	// dist-native 文件名不含版本号，校验 checksums.txt 与二进制一致，避免混入残留旧构建。
	checksums := map[string]string{}
	for _, a := range artifacts {
		if a.Filename != "checksums.txt" {
			checksums[a.Filename] = a.SHA256
		}
	}
	data, err := os.ReadFile(filepath.Join(distDir, "checksums.txt"))
	if err != nil {
		fatalf("读取 checksums.txt: %v", err)
	}
	seen := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		expected, ok := checksums[name]
		if !ok {
			fatalf("checksums.txt 出现未知条目: %s", name)
		}
		if fields[0] != expected {
			fatalf("%s 与 checksums.txt 不一致（构建产物残留？）", name)
		}
		seen++
	}
	if seen != len(checksums) {
		fatalf("checksums.txt 条目数 %d 与二进制数 %d 不一致", seen, len(checksums))
	}
	return artifacts
}

func renderInstaller(root, filename, installBaseURL string) []byte {
	path := filepath.Join(root, "scripts", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("读取安装脚本 %s: %v", filename, err)
	}
	source := string(data)
	for _, sentinel := range []string{sentinelBaseURL, sentinelRendered} {
		if !strings.Contains(source, sentinel) {
			fatalf("scripts/%s 缺少占位符 %s", filename, sentinel)
		}
	}
	source = strings.ReplaceAll(source, sentinelBaseURL, installBaseURL)
	source = strings.ReplaceAll(source, sentinelRendered, "1")
	source = strings.ReplaceAll(source, rawInstallerURL, installBaseURL+"/install.sh")
	source = strings.ReplaceAll(source, rawInstallerPS1URL, installBaseURL+"/install.ps1")
	return []byte(source)
}

func renderWuyingInstaller(root, installBaseURL, version string) []byte {
	path := filepath.Join(root, "scripts", "install-wuying.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("读取无影安装脚本: %v", err)
	}
	source := string(data)
	for _, sentinel := range []string{sentinelBaseURL, sentinelRendered, sentinelReleaseVersion} {
		if !strings.Contains(source, sentinel) {
			fatalf("scripts/install-wuying.sh 缺少占位符 %s", sentinel)
		}
	}
	source = strings.ReplaceAll(source, sentinelBaseURL, installBaseURL)
	source = strings.ReplaceAll(source, sentinelRendered, "1")
	source = strings.ReplaceAll(source, sentinelReleaseVersion, version)
	source = strings.ReplaceAll(source, rawWuyingInstallerURL, installBaseURL+"/install-wuying.sh")
	return []byte(source)
}

// isPrerelease 判断版本号是否带 SemVer 预发布后缀（0.10.0-beta.1）。预发布版本
// 不允许出现在任何默认安装路径上。
func isPrerelease(version string) bool {
	return strings.Contains(version, "-")
}

// installerUploadKeys 返回一个安装脚本本次要写入的 OSS key。正式版同时刷新
// live 与版本归档；预发布版只写归档，避免 beta 顶掉 live 脚本，让不带版本号的
// curl 一键安装意外装到预发布版。
func installerUploadKeys(prefix, version, filename string) []string {
	keys := []string{joinKey(prefix, "v"+version, "installer", filename)}
	if !isPrerelease(version) {
		keys = append(keys, joinKey(prefix, filename))
	}
	return keys
}

func main() {
	versionFlag := flag.String("version", "", "发布版本（默认读 package.json）")
	only := flag.String("only", "", "只上传某类内容: install | artifacts")
	dryRun := flag.Bool("dry-run", false, "只打印将要上传的内容，不实际上传")
	flag.Parse()

	if *only != "" && *only != "install" && *only != "artifacts" {
		fatalf("--only 只能是 install 或 artifacts")
	}

	root := findRepoRoot()
	loadDotEnv(root)

	version := *versionFlag
	if version == "" {
		version = packageVersion(root)
	}
	pkgVersion := packageVersion(root)
	if version != pkgVersion {
		fatalf("--version %s 与 package.json %s 不一致", version, pkgVersion)
	}

	prefix := envOr("OSS_PREFIX", defaultPrefix)
	publicURL := strings.TrimRight(envOr("OSS_PUBLIC_URL", defaultPublicURL), "/")
	installBaseURL := publicURL + "/" + strings.Trim(prefix, "/")
	distDir := envOr("DIST_DIR", filepath.Join(root, "dist-native"))
	prerelease := isPrerelease(version)
	channel := "latest"
	if prerelease {
		channel = "beta"
	}

	u := &uploader{
		bucket:       envOr("OSS_BUCKET", defaultBucket),
		publicURL:    publicURL,
		acl:          strings.TrimSpace(os.Getenv("OSS_OBJECT_ACL")),
		cacheControl: envOr("OSS_CACHE_CONTROL", defaultCacheControl),
		dryRun:       *dryRun,
	}
	if !*dryRun {
		region := strings.TrimPrefix(requireEnv("OSS_REGION"), "oss-")
		requireEnv("OSS_ACCESS_KEY_ID")
		requireEnv("OSS_ACCESS_KEY_SECRET")
		cfg := oss.LoadDefaultConfig().
			WithCredentialsProvider(credentials.NewEnvironmentVariableCredentialsProvider()).
			WithRegion(region).
			WithRetryMaxAttempts(maxAttempts)
		if endpoint := strings.TrimSpace(os.Getenv("OSS_ENDPOINT")); endpoint != "" {
			cfg = cfg.WithEndpoint(endpoint)
		}
		u.client = oss.NewClient(cfg)
	}

	fmt.Println()
	logf("@yoooclaw/cli v%s OSS upload%s", version, map[bool]string{true: " (dry run)", false: ""}[*dryRun])
	logf("bucket: %s  prefix: %s  channel: %s", u.bucket, prefix, channel)
	logf("install base URL: %s", installBaseURL)
	fmt.Println()

	if *only == "" || *only == "install" {
		if prerelease {
			logf("预发布版本：只写 v%s 归档，跳过 live 安装脚本与渠道标记", version)
		}
		for _, filename := range []string{"install.sh", "install.ps1", "install-wuying.sh"} {
			var rendered []byte
			if filename == "install-wuying.sh" {
				rendered = renderWuyingInstaller(root, installBaseURL, version)
			} else {
				rendered = renderInstaller(root, filename, installBaseURL)
			}
			for _, key := range installerUploadKeys(prefix, version, filename) {
				u.put(key, rendered, contentTypeFor(filename), strings.TrimPrefix(key, prefix+"/"))
			}
		}
	}

	if *only == "" || *only == "artifacts" {
		artifacts := collectArtifacts(distDir, prefix, publicURL, version)
		for _, a := range artifacts {
			data, err := os.ReadFile(a.localPath)
			if err != nil {
				fatalf("读取 %s: %v", a.localPath, err)
			}
			u.put(a.Key, data, a.ContentType, a.Filename)
		}

		var checksumsURL string
		for _, a := range artifacts {
			if a.Filename == "checksums.txt" {
				checksumsURL = a.URL
			}
		}
		manifest := map[string]any{
			"package":                    "@yoooclaw/cli",
			"version":                    version,
			"channel":                    channel,
			"installScriptUrl":           urlForKey(publicURL, joinKey(prefix, "install.sh")),
			"wuyingInstallScriptUrl":     urlForKey(publicURL, joinKey(prefix, "install-wuying.sh")),
			"installerArchiveUrl":        urlForKey(publicURL, joinKey(prefix, "v"+version, "installer", "install.sh")),
			"wuyingInstallerArchiveUrl":  urlForKey(publicURL, joinKey(prefix, "v"+version, "installer", "install-wuying.sh")),
			"windowsInstallScriptUrl":    urlForKey(publicURL, joinKey(prefix, "install.ps1")),
			"windowsInstallerArchiveUrl": urlForKey(publicURL, joinKey(prefix, "v"+version, "installer", "install.ps1")),
			"artifactBaseUrl":            urlForKey(publicURL, joinKey(prefix, "v"+version)),
			"checksumsUrl":               checksumsURL,
			"artifacts":                  artifacts,
		}
		manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			fatalf("生成 oss-manifest.json: %v", err)
		}
		u.put(
			joinKey(prefix, "v"+version, "oss-manifest.json"),
			append(manifestJSON, '\n'),
			contentTypeFor("oss-manifest.json"),
			"oss-manifest.json",
		)
		if !prerelease {
			u.put(joinKey(prefix, channel), []byte(version+"\n"), "text/plain; charset=utf-8", channel)
		}
	}

	fmt.Println()
	if prerelease {
		okf("installer 归档: %s/installer/", urlForKey(publicURL, joinKey(prefix, "v"+version)))
		okf("预发布版本未改动 live 安装脚本与 latest 标记；安装需显式 --version %s", version)
	} else {
		okf("installer: %s", urlForKey(publicURL, joinKey(prefix, "install.sh")))
		okf("wuying installer: %s", urlForKey(publicURL, joinKey(prefix, "install-wuying.sh")))
		okf("Windows installer: %s", urlForKey(publicURL, joinKey(prefix, "install.ps1")))
	}
	okf("artifacts: %s/", urlForKey(publicURL, joinKey(prefix, "v"+version)))
	if !prerelease {
		okf("%s marker: %s", channel, urlForKey(publicURL, joinKey(prefix, channel)))
	}
	okf("OSS upload complete")
	fmt.Println()
}
