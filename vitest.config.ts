import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // 行为测试会拉起真实 daemon 子进程（~1.5s 启动）+ 进程生命周期，默认 5s 不够。
    testTimeout: 30_000,
    hookTimeout: 30_000,
    // daemon e2e / lifecycle 各自独立持有端口与 home，文件级并行即可；
    // 单文件内顺序执行，避免共享 home 的用例相互踩。
    fileParallelism: true,
  },
});
