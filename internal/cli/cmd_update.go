package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/version"
	"github.com/spf13/cobra"
)

const (
	updatePackage   = "@yoooclaw/cli"
	updateRegistry  = "https://registry.npmjs.org"
	updateInstallSh = "https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh"
)

func newUpdateCmd() *cobra.Command {
	c := &cobra.Command{Use: "update", Short: "版本检查 🟢"}
	self := &cobra.Command{Use: "self", Short: "检查 npm 最新版本并提示（不自动更新）", Args: cobra.NoArgs, RunE: run(updateSelf)}
	self.Flags().Bool("beta", false, "检查 beta channel")
	self.Flags().Bool("json", false, "只输出版本信息 JSON")
	c.AddCommand(self)
	return c
}

func nativeTargetName() string {
	os, arch := runtime.GOOS, runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	if (os == "darwin" || os == "linux") && (arch == "arm64" || arch == "x64") {
		return "yoooclaw-" + os + "-" + arch
	}
	return ""
}

func upgradeCommand(channel, latest string) string {
	if version.Dist() == "native" {
		if nativeTargetName() == "" {
			return "curl -fsSL " + updateInstallSh + " | sh"
		}
		return "curl -fsSL " + updateInstallSh + " | sh -s -- --version " + latest
	}
	if channel == "beta" {
		return "npm i -g " + updatePackage + "@beta"
	}
	return "npm update -g " + updatePackage
}

// compareSemver 比较 x.y.z（忽略 prerelease）；a>b 返回 1，a<b 返回 -1，相等 0。
func compareSemver(a, b string) int {
	pa := semverParts(a)
	pb := semverParts(b)
	for i := 0; i < 3; i++ {
		if d := pa[i] - pb[i]; d != 0 {
			if d > 0 {
				return 1
			}
			return -1
		}
	}
	return 0
}

func semverParts(v string) [3]int {
	core := strings.SplitN(v, "-", 2)[0]
	var out [3]int
	for i, seg := range strings.SplitN(core, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(seg)
		out[i] = n
	}
	return out
}

func updateSelf(_ *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	tag := "latest"
	if flagBool(cmd, "beta") {
		tag = "beta"
	}
	ctxReq, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, updateRegistry+"/"+encodePkgName(updatePackage), nil)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "构造 npm 请求失败")
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "查询 npm registry 失败："+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errs.New(errs.CodeNetworkError, "npm registry 返回 "+strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "读取 npm registry 响应失败")
	}
	var parsed struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errs.New(errs.CodeNetworkError, "解析 npm registry 响应失败")
	}
	latest, ok := parsed.DistTags[tag]
	if !ok || latest == "" {
		return nil, errs.New(errs.CodeNotFound, "npm dist-tag `"+tag+"` 不存在")
	}

	updateAvailable := compareSemver(latest, version.Version) > 0
	var command any
	hint := "已是最新版本"
	if updateAvailable {
		command = upgradeCommand(tag, latest)
		hint = "发现新版本；CLI 不做自动更新，请手动执行上面的命令"
	}
	return map[string]any{
		"ok": true, "package": updatePackage, "channel": tag, "dist": version.Dist(),
		"current": version.Version, "latest": latest, "updateAvailable": updateAvailable,
		"command": command, "hint": hint,
	}, nil
}

// encodePkgName 对 @scope/name 做最简 path 编码（/ 转义）。
func encodePkgName(pkg string) string {
	return strings.ReplaceAll(pkg, "/", "%2F")
}
