.PHONY: test test-race cover cover-gate cover-html test-int vet update-golden ci

# 覆盖率门禁阈值（可在命令行覆盖：make cover-gate MIN_COVERAGE=60）
MIN_COVERAGE ?= 55

# 默认：跑全部单元测试（带竞态检测）
test:
	go test -race ./...

# 仅竞态检测，详细输出
test-race:
	go test -race -v ./...

# 生成覆盖率 profile 并打印按函数/总计的统计
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# 覆盖率门禁（只许涨不许跌）；调高 MIN_COVERAGE 收紧
cover-gate: cover
	MIN_COVERAGE=$(MIN_COVERAGE) scripts/coverage-gate.sh

# 在浏览器查看覆盖率（生成 HTML 报告）
cover-html: cover
	go tool cover -html=coverage.out -o coverage.html
	@echo "已生成 coverage.html"

# 跑带 integration tag 的测试（真实钥匙串 / 进程级，本地手动跑）
test-int:
	go test -race -tags=integration ./...

# 刷新 golden 期望文件
update-golden:
	go test ./... -update

vet:
	go vet ./...

# 本地复刻 CI：vet + race + 覆盖率门禁
ci: vet cover-gate
