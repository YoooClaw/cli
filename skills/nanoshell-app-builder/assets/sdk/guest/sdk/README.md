# Guest SDK

| 路径 | 用法 |
|------|------|
| **统一 Guest** | `#include "ns_guest.h"` → `guest/wasm/<name>.c` |
| 双后端 | **Wasm**（默认）：`pack-nsp` / web-preview<br>**Native builtin**：`-DNS_GUEST_NATIVE=1` + `ns_guest_native.c` |
| 入口 | `NS_GUEST_EXPORT void start(void)` + 文末 `NS_GUEST_BUILTIN(name)` |
| 兼容 | `guest/wasm/ns_abi.h` → 转调 `ns_guest.h` |

**写 App → [docs/guest-app-guide.md](../../docs/guest-app-guide.md)**  

便携快照的构建、自动测试、试玩和交付按 skill 的 SKILL.md 与 references/environment.md 执行；原生 builtin 所需文件不在本快照中。
