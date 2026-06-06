package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/keychain"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/version"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	c := &cobra.Command{Use: "doctor", Short: "本地环境自检：运行时/目录/keychain/config/daemon 🟢", Args: cobra.NoArgs, RunE: run(doctor)}
	c.Flags().Bool("json", false, "JSON 输出（给脚本用）")
	c.Flags().Bool("fix", false, "自动修复可修复的问题")
	return c
}

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | skip
	Detail string `json:"detail"`
}

func checkDir(name, dir string, fix bool) check {
	if !fsutil.Exists(dir) {
		if fix {
			if err := fsutil.EnsureDir(dir, fsutil.DirMode); err == nil {
				return check{name, "ok", "已创建 " + dir}
			}
		}
		return check{name, "warn", "目录不存在：" + dir + "（--fix 可创建）"}
	}
	probe := filepath.Join(dir, ".yc-doctor-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return check{name, "fail", "目录不可读写：" + dir}
	}
	_ = os.Remove(probe)
	info, err := os.Stat(dir)
	if err != nil {
		return check{name, "fail", "无法 stat：" + dir}
	}
	mode := info.Mode().Perm()
	tooOpen := mode&0o077 != 0
	detail := dir + "（mode " + strconv.FormatUint(uint64(mode), 8) + ")"
	if tooOpen {
		return check{name, "warn", dir + "（mode " + strconv.FormatUint(uint64(mode), 8) + "，建议收紧到 700）"}
	}
	return check{name, "ok", detail}
}

func doctor(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	fix := flagBool(cmd, "fix")
	checks := []check{
		{"runtime", "ok", "原生二进制 " + version.Version + "（" + runtime.GOOS + "/" + runtime.GOARCH + "，无 Node 依赖）"},
		checkDir("root-dir", paths.RootDir(), fix),
	}

	if config.Exists(ctx.Paths) {
		if _, err := config.Load(ctx.Paths); err != nil {
			checks = append(checks, check{"profile-config", "fail", err.Error()})
		} else {
			checks = append(checks, check{"profile-config", "ok", ctx.Paths.Config + " 可解析"})
		}
	} else {
		checks = append(checks, check{"profile-config", "warn", "profile `" + ctx.Profile + "` 未初始化（yoooclaw config init）"})
	}

	apiKey := creds.ResolveAPIKey()
	if apiKey.Value != "" {
		checks = append(checks, check{"api-key", "ok", "来源 " + apiKey.Source})
	} else {
		checks = append(checks, check{"api-key", "warn", "未配置（yoooclaw auth set-api-key）"})
	}

	if config.Exists(ctx.Paths) {
		if cfg, err := config.Load(ctx.Paths); err == nil {
			token, _ := creds.ResolveGatewayToken(cfg)
			if token.Value != "" {
				checks = append(checks, check{"gateway-token", "ok", "来源 " + token.Source})
			} else {
				checks = append(checks, check{"gateway-token", "warn", "未设置（yoooclaw auth token-rotate）"})
			}
		}
	}

	if keychain.Available() {
		checks = append(checks, check{"keychain", "ok", "可用"})
	} else {
		checks = append(checks, check{"keychain", "skip", "当前平台无可用 keychain，凭据将落文件"})
	}

	state := daemon.State(ctx.Paths)
	switch {
	case state.Running:
		checks = append(checks, check{"daemon", "ok", "运行中（pid " + strconv.Itoa(state.Lock.PID) + "）"})
	case state.Stale:
		checks = append(checks, check{"daemon", "warn", "锁文件存在但进程已死（陈旧锁）"})
	default:
		checks = append(checks, check{"daemon", "skip", "未运行"})
	}

	failed, warned := 0, 0
	checksAny := make([]any, 0, len(checks))
	for _, c := range checks {
		if c.Status == "fail" {
			failed++
		}
		if c.Status == "warn" {
			warned++
		}
		checksAny = append(checksAny, map[string]any{"name": c.Name, "status": c.Status, "detail": c.Detail})
	}
	return map[string]any{
		"ok": failed == 0, "profile": ctx.Profile,
		"summary": map[string]any{"total": len(checks), "failed": failed, "warned": warned},
		"checks":  checksAny,
		"note":    "网络类自检（relay / OSS 可达性）请用 yoooclaw gateway test / tunnel +test",
	}, nil
}
