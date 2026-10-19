# CI 与发布

## 前提

- 仓库已公开于 [chisaato/arr-download-proxy](https://github.com/chisaato/arr-download-proxy)，`origin` 已配置。工作流随提交推送后由 GitHub Actions 执行。
- Go 版本由 `go.mod` 决定，当前为 **1.26.7**。SQLite 使用 `modernc.org/sqlite`；发布构建使用 `CGO_ENABLED=0`，不需要交叉 C 编译器。
- 在 GitHub 仓库设置中启用 Actions，组织策略需允许工作流使用 `GITHUB_TOKEN` 写入 Releases 与 GHCR Packages。只有标签发布的 `publish` job 请求 `contents: write` 和 `packages: write`；main 的 `publish-dev` job 仅请求 `contents: read` 和 `packages: write`；不需要额外 PAT。
- GHCR 包默认可能是私有的；需要匿名拉取时，在包设置中设为 Public。若已存在同名包，需将其连接至本仓库并授予本仓库 Actions 写入权限。
- 本项目采用 [MIT License](../LICENSE)；根目录 `LICENSE` 会由发布脚本放入归档，分发时应保留版权与许可声明。

分支 push 与 pull request 执行测试、`go vet` 和构建。只有 **main push 且检查通过**后，才登录 GHCR 发布 `ghcr.io/chisaato/arr-download-proxy:dev`，包含 `linux/amd64` 与 `linux/arm64`。其他分支 push、所有 PR（包括外部 fork）只检查，不登录、不推送；不使用 `pull_request_target`。Checkout 禁止持久化凭据。

同一分支的新 CI 会取消旧 CI（包括未完成的 dev 发布），优先保留最新提交的构建。main 只更新 `:dev`，不更新 `:latest`；标签发布只更新版本标签与 `:latest`，不更新 `:dev`。标签由独立 Release 工作流检查，不重复触发分支 CI。标签发布串行执行。

## 发布标签

在希望发布的提交上执行（以下命令由维护者确认后执行）：

```bash
git push origin HEAD
git tag v1.2.3
git push origin v1.2.3
```

标签必须是带 `v` 前缀的合法 SemVer，例如 `v1.2.3`、`v1.3.0-rc.1`；`vfoobar`、`v01.2.3`、`v1.2.3-01` 会失败。**所有合法版本标签（包括预发布）都会在发行成功后更新镜像 `:latest`**。因此镜像 `latest` 可能指向 RC 等预发布版本；需要固定版本时请使用具体版本标签。回补旧版本也会更新镜像 `latest`，请谨慎操作。

不要混淆两个 latest：GitHub Release 仍将预发布标记为 prerelease，并设置 `--latest=false`，不会取代 GitHub 最新稳定发行版；这不影响 GHCR 镜像 `:latest` 的更新。支持 SemVer 构建元数据；镜像标签将 `+` 替换为 `_`（例如 `v1.2.3+build.1` → `v1.2.3_build.1`），长度最多 128 字符。

发布会先重新运行测试/vet，再从标签提交构建：

- `ghcr.io/<小写 owner>/<小写 repository>:<版本标签>`：包含 `linux/amd64` 与 `linux/arm64` 的镜像；稳定版及预发布版都会更新同一镜像的 `:latest`。镜像路径从仓库名转为小写推导，不硬编码 owner。
- GitHub Release：`arr-download-proxy_v1.2.3_linux_amd64.tar.gz`、`arr-download-proxy_v1.2.3_linux_arm64.tar.gz` 和 `SHA256SUMS`，附简洁发行说明。

镜像与 GitHub Release 不具备跨服务事务性；失败时可能留下已推送的版本镜像。查看 Actions 日志，确认失败原因后处理，不要盲目移动或覆盖已发布标签。Release 已创建后的重跑不会覆盖已有 Release，会显式失败。

## 本地构建与校验

需要 Bash、Go、tar 与 GNU `sha256sum`；无需 Git 历史或 GoReleaser。以下只生成本地产物，不发布、也不启动服务：

```bash
bash scripts/release.sh v1.2.3 /tmp/opencode/arr-download-proxy-release
cd /tmp/opencode/arr-download-proxy-release
sha256sum -c SHA256SUMS
tar -tzf arr-download-proxy_v1.2.3_linux_amd64.tar.gz
```

省略输出目录时脚本创建系统临时目录并打印路径，不向仓库根目录写入产物。已有同名归档或 `SHA256SUMS` 会拒绝覆盖。两种架构各有一个顶层目录，包含 `arr-download-proxy`、`config.example.yaml`，以及存在时的 `LICENSE`。校验和用于检查下载完整性，不是独立签名。

选择符合机器架构的归档，校验后解压，复制并编辑配置。只运行 `--help` 不会启动服务：

```bash
tar -xzf arr-download-proxy_v1.2.3_linux_amd64.tar.gz
cd arr-download-proxy_v1.2.3_linux_amd64
./arr-download-proxy --help
cp config.example.yaml config.yaml
# 编辑 config.yaml 后再启动；-c 等同于 --config。
./arr-download-proxy --config ./config.yaml
# 需要轮询明细时使用：
./arr-download-proxy -c ./config.yaml --debug
```

没有额外的版本命令或配置环境变量接口。运行时应明确指定配置路径，并按实际部署设置数据库与下载路径；不要直接使用示例中的连接信息启动生产服务。
