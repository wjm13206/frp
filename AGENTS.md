# AGENTS.md

> 本仓库是 ChmlFrp 客户端 fork（基于 frp v0.71.0），**只有 frpc，没有 frps**。上游文档/脚本中提到 frps 的部分均为过期残留，不要照做。

## 结构（只看这几个）

- 入口：`cmd/frpc/main.go` → `cmd/frpc/sub/root.go`（`Execute()`）
- 核心：`client/service.go`（`client.NewService` + `Run`），配置聚合在 `root.go` 的 `runClientWithAggregator`
- 配置：`pkg/config/v1/` + `pkg/config/source/`（Aggregator / StoreSource），校验在 `pkg/config/v1/validation`
- ChmlFrp 定制：`pkg/api`（`-u/--token` + `-p/--id` 从 `https://cf-v2.uapis.cn/cfg` 拉配置写入 `-c` 文件）、`pkg/policy/security`（`--allow-unsafe`）、`--strict_config`（默认 true）
- 已删除（别找了）：`server/`、`cmd/frps/`、`test/e2e/`、`conf/frps*.toml`、`web/frps/`

## 构建

- 只用 `make frpc`（产物 `bin/frpc`，`CGO_ENABLED=0`）。**不要用** `make frps` / `make build` / `make all` / `make web` / `make e2e*`：目标引用了已删除的 `cmd/frps`、`test/e2e`、`web/frps`，必失败。
- `Makefile` 的 `NOWEB_TAG` 逻辑：`web/frpc/dist` 不存在时自动加 `,noweb` tag。当前两个 `dist` 都不存在，直接 `go build/vet/test` 需手动加 `-tags noweb`（`make frpc` 已处理）。
- 前端只剩 frpc：`make -C web/frpc build`。根 `web/package.json` 的 `workspaces` 里还写着已删除的 `frps`，`make web-ci` 会失败，按需修了再用。

## 测试与校验

- `make gotest` / `make alltest` 已坏（仍引用不存在的 `./server/...` 和 `e2e`）。用聚焦命令代替：
  - 单包：`go test -tags noweb ./pkg/config/...`（已验证可过）
  - 客户端：`go test -tags noweb ./client/... ./pkg/util/...`
- 提交前顺序：`make fmt` → `make gci` → `make vet`（`vet` 自带 `NOWEB_TAG`，可直接用）。lint 配置见 `.golangci.yml`：`gofumpt` + `gci`（分组 `standard,default,prefix(github.com/fatedier/frp/)`）+ 行宽 160（`lll`）。
- Go 1.25，CI（`.github/workflows/golangci-lint.yml`）跑在 `master`/`dev`，含 `make web-ci`，本地改前端后先保证 `web` 能 build。

## 配置约定

- 新配置用 `toml`/`yaml`/`json`（示例 `conf/frpc.toml` / `conf/frpc_full_example.toml`）；`ini` 仅 legacy，会打印 deprecation 警告。未知字段默认报错（`--strict_config`）。
- 发版流程 `doc/agents/release.md` 是上游原文（含 frps 构建、e2e、兼容性矩阵、`dev`→`master` merge commit、手动触发 goreleaser），在本 fork 仅供参考，按实际 CI/产物裁剪后执行。
