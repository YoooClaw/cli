/**
 * yoooclaw CLI 可执行入口。
 * package.json#bin（yoooclaw / yc）指向构建产物 dist/bin.cjs。
 */
import { run } from "./index.js";

void run(process.argv);
