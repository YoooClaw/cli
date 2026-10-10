# 试玩服务与打开方式

## 本地与云电脑

本地 Agent 可用宿主支持的持久终端运行 `pipeline.py serve`。YoooClaw / 无影云电脑会回收 Agent 执行环境的子进程；普通后台任务、`nohup`、`setsid` 都不能作为跨回合存活的保证。此环境优先使用 **systemd 用户服务**托管，保持到用户结束试玩。

预览服务使用项目的 `tools/web-preview/serve.py`，保留 COOP/COEP 头。自动验收脚本的临时服务器会在测试结束时关闭，不能把它的地址交给用户试玩。

## 云电脑：systemd 用户服务

以运行 Agent 的普通用户操作，先设置会话变量并检查用户服务管理器：

```bash
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=$XDG_RUNTIME_DIR/bus}"
systemctl --user show-environment >/dev/null
```

使用项目专属名称，例如 `nanoshell-preview-flappy.service`。先检查现有服务及端口占用：相同项目复用原服务；不同项目使用独立服务和空闲端口。不要停止不属于当前项目的进程。

在 `~/.config/systemd/user/<服务名>.service` 写入下面模板。替换全部占位值；Python、工作目录及脚本都用实际绝对路径，路径中的 `%` 按 systemd 规则转义为 `%%`。

```ini
[Unit]
Description=NanoShell app preview

[Service]
Type=simple
WorkingDirectory="<项目绝对路径>"
ExecStart="<Python绝对路径>" -u "<项目绝对路径>/tools/web-preview/serve.py" --port <端口> --bind 127.0.0.1
Restart=on-failure
RestartSec=2
```

```bash
systemctl --user daemon-reload
systemctl --user start "$NS_UNIT"
systemctl --user is-active "$NS_UNIT"
```

`NS_UNIT` 为已创建的服务名。服务文件修改后用 `restart`；无须为了本次试玩设置开机自启。用户服务由 systemd 管理，不依赖当前 Agent 命令。若用户服务管理器不可用，使用平台明确提供的持久服务托管；报告具体阻塞，不反复采用已被回收的后台方式并宣称修复。

默认绑定回环地址；跨设备访问需要平台实际支持的转发入口。绑定 `0.0.0.0` 本身不会产生可访问的公网链接，局域网 HTTP 也可能不满足 SharedArrayBuffer 的安全上下文要求。

## 交付前验证

结束启动命令后，在**另一条工具调用**中检查：

1. `systemctl --user is-active "$NS_UNIT"` 为 active，`show` 中 ExecStart 指向当前项目；端口仍监听。
2. 主页、`/dist/catalog.json` 和目标 `/dist/<name>.nsp/app.wasm` 均 HTTP 200；Wasm 哈希等于已验收的候选包。
3. 在最终使用的浏览器入口打开页面，确认 `crossOriginIsolated=true`、应用可运行、OK/BACK 有效。实际试玩服务在检查完成后保持运行。

这些检查通过后才说“试玩已就绪”。仍打不开时查看 `journalctl --user -u "$NS_UNIT" -n 50 --no-pager`，区分进程退出、端口错误、浏览器打开位置错误和缺少跨源隔离头。修复后重查，不用截图代替用户要求的可交互试玩。

用户要求结束试玩时停止当前项目服务；正常会话结束不要停止试玩服务。重启机器或用户会话结束后的服务可用性需重新验证，不承诺永久在线。

## 给用户的文案

云电脑场景使用下面的结构，端口、应用名和按键替换为本次实际值：

> 试玩已就绪：[点击试玩](http://127.0.0.1:<端口>/)。
>
> **请选择“安全方式打开”，不要选择“新窗口打开”。** 需要在云电脑里的浏览器访问；也可以进入云电脑桌面，在浏览器地址栏粘贴上面的地址。
>
> 页面中点击「<应用名> → 运行」。Enter / 空格为 OK，Esc 为 BACK；也可使用页面按钮。预览服务已由系统托管，Agent 回复结束后仍可试玩。
>
> 试玩满意后告诉我，我再交付安装包。

`127.0.0.1` 指访问它的那台电脑；不要引导用户在自己手机或本机的新窗口访问云电脑回环地址。若平台提供了经过验证的转发预览 URL，交付该实际 URL，并仍提醒云电脑界面的安全打开选项。安全打开指平台入口选择，不是关闭浏览器安全保护。
