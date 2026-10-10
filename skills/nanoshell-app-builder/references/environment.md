# 环境与命令

下文 `SKILL` 是当前 SKILL.md 所在目录，`PROJECT` 是用户项目绝对路径，`NAME` 为小写字母开头、由小写字母/数字/下划线组成的应用名。执行时替换占位值并正确引用路径；不要写死作者电脑路径。

```bash
python3 "$SKILL/scripts/pipeline.py" init --project "$PROJECT"
python3 "$SKILL/scripts/pipeline.py" build --project "$PROJECT" --name "$NAME" --id com.example.myapp --title "My App"
python3 "$SKILL/scripts/pipeline.py" serve --project "$PROJECT" --port 18766
```

`init` 只接受不存在的目录。项目使用 skill 随附的版本固定 SDK；`assets/sdk/snapshot.json` 记录来源。原始仓库不参与使用者的构建。要采用新 Host ABI，应先更新快照并重做兼容性验证。

## 版本固定与跨项目共享

`dependencies.json` 是 skill 的依赖版本清单；项目初始化时复制为 `nanoshell-deps.lock.json`。版本有三层：NanoShell 接口快照由 `snapshot.json` 的提交号固定，WASI SDK 编译工具链及 Playwright 由依赖锁固定。

```bash
python3 "$SKILL/scripts/dependencies.py" setup --project "$PROJECT"
```

脚本在当前用户的 `~/.local/share/nanoshell/` 下按“依赖名称 / 精确版本 / 系统架构”管理安装。可通过 `NS_DEPENDENCY_HOME` 更改共享位置。查找次序：显式环境变量 → 共享登记 → 当前项目及同级旧项目。找到版本一致且能运行的现有安装时只登记引用，不复制、不重新下载；原目录消失后会重新安装缺失版本。新安装放到共享目录，多项目同版本并发安装通过文件锁串行化。

Linux x86_64 的 WASI SDK 优先使用清单中的 Yoooclaw OSS，失败才回退同版本官方地址；下载包必须匹配清单固定的官方 SHA-256，校验失败直接停止。其他已配置平台使用清单中的官方文件。Playwright 使用精确版本安装，复用系统 Chrome；不是每个项目执行 `npm install`。

安装失败时先读取完整错误日志，区分包初始化、下载、校验和运行验证失败，再修复对应步骤。共享安装器直接生成合法 `package.json`；手动排障时也使用合法包名，`npm init -y` 在 `.qa` 目录会因默认包名 `.qa` 非法而失败。保留 stderr 与原始退出码，避免隐藏报错后盲目重试。

`pipeline.py build` 与 `test_app.mjs` 自动读取项目锁和共享依赖路径，不依赖前一个 shell 的 `export`。依赖缺失时运行 setup；仅显示当前解析结果可执行 `dependencies.py env --project "$PROJECT"`。显式指定 `NS_CLANG` / `NS_PLAYWRIGHT_MODULE` 也要符合项目锁定版本，否则报错而不静默改用另一版本。

### 更新与旧项目

维护者升级依赖时修改 skill 的 `dependencies.json`（版本、各架构 URL、SHA-256），先通过完整集成测试，再发布 skill。新项目采用新清单；已有项目继续读取原锁，不因更新 skill 自动升级。需要升级已有项目时执行：

```bash
python3 "$SKILL/scripts/dependencies.py" upgrade --project "$PROJECT"
```

该命令备份原锁后采用当前 skill 的清单，安装或复用新版本；旧版保留供其他项目使用。随后必须重新构建、测试和试玩；报告绑定依赖锁，旧报告不能发布。升级失败可用 `.bak` 恢复原锁。只有用户明确要求清理时才删除无用旧版本。NanoShell 接口快照迁移见下文，依赖升级命令不会改写应用源码或 Host API 快照。

### 自动测试浏览器

Python 3.10+、Node.js 20+、curl 与 npm 由运行环境提供；共享依赖安装器目前支持 Linux/macOS。Windows 环境需要另行配置经验证的工具链。

保留用户显式设置的 `NS_CHROMIUM_EXECUTABLE`；否则自动检测 `/opt/google/chrome/chrome`。以 Playwright 实际启动及验收结果判断兼容性。没有可用系统浏览器时才使用解析出的 Playwright 包的 `cli.js install chromium`，浏览器自身使用用户级缓存。应用断言失败应修复应用，不触发浏览器重装。浏览器与 Node 的实际版本记录在测试报告里。

预览服务需 COOP/COEP 响应头，使用项目提供的 `serve.py`。`--port 0` 可分配空闲端口，读取实际输出的 URL。保留预览进程供试玩；测试脚本的临时服务会自行结束。

## 基础设备配置

当前快照面向 AiNote NanoShell：240×120、OK/BACK 两键。Wasm 本体 ≤12288 字节；本流程默认附加 16 字节 NSP1 头（文件总长 ≤12304 字节）。线性内存上限为 8192 字节，编译器将 Guest C 栈设置为 3072 字节，静态数据与栈必须共同容纳在线性内存中。`poll_key()` 仅返回 DOWN；长按 REPEAT 和松开 UP 通过 `event_poll()` 处理。新应用入口使用 `guest/sdk/ns_guest.h`；`guest/wasm/ns_abi.h` 为兼容封装。打包器通过 `--stack-first` 将 C 栈放在低地址。详细接口、内存/计时器/绘图限额以随项目复制的头文件和 ABI 文档为准。面向其他硬件时先取得对应 Host 配置，不把所有带屏设备视为兼容。

## 从旧版项目迁移

旧项目继续使用其已固定的 SDK；更新已安装 skill 不会自动修改这些项目。要迁移，先保留源码和原项目，用新版 `pipeline.py init` 创建新目录，再迁入自定义应用、`acceptance.md` 和测试场景。重新构建、验收并请用户试玩。预览和模拟器改为读取 `dist/*.nsp` 与 `dist/catalog.json`，无需 `samples/` 副本；旧测试报告和用户认可不沿用。

当前快照要求支持 12 KiB 模块、8 KiB 线性内存和 6 个计时器的新 Host；旧固件不能仅靠升级 skill 获得这些容量。WASI SDK 编译器仍为 34.0，无需因 Host 预算变化重新安装。FixedHeap 仍为 16 KiB，不得自行提高。

开发指南随附于 `assets/sdk/docs/guest-app-guide.md`（初始化后为 `docs/guest-app-guide.md`）。其中原生 builtin 接线和 Windows 批处理属于完整上游仓库；本 skill 使用 pipeline 的 Wasm 路径，最小模板参考 `guest/wasm/hello_tiny.c`。`snapshot.json` 的 `sourceCommit` 是上游基线，`files` 为实际分发文件哈希，`provenance` 区分上游原文件、本地适配和本地新增，不能仅凭 DIFF 判定快照过期。
