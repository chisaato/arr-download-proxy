# Docker 部署

## 镜像约定

根目录 `Dockerfile` 使用多阶段构建，入口为 `./cmd/server`。Go 版本与
`go.mod` 的 `go 1.26.7` 对齐；SQLite 使用 `modernc.org/sqlite` 纯 Go
实现，设置 `CGO_ENABLED=0`，通过 buildx 的 `TARGETOS` / `TARGETARCH`
交叉编译 Linux amd64、arm64，不需要传自定义构建参数。

运行层为 Alpine，含 CA 证书与时区数据库；默认 UTC，可设置 `TZ=Asia/Shanghai`
等时区。默认用户为 `10001:10001`，工作目录 `/app`，程序作为 PID 1
直接接收 SIGTERM。默认参数为 `--config /config/config.yaml`；也支持 `-c`
与 `--debug`。镜像不包含配置模板、密码或 SQLite 数据。

```bash
# 本机构建；不会启动代理。
docker buildx build --load -t arr-download-proxy:local .
docker run --rm --network none arr-download-proxy:local --help

# 双架构构建并推送；先替换仓库与版本，登录自己的镜像仓库。
# 不需要跨架构运行 Go 编译器，但运行层 apk 步骤需要构建器支持目标架构。
docker buildx build --platform linux/amd64,linux/arm64 \
  -t <registry>/<namespace>/arr-download-proxy:<version> --push .
```

尖括号内容是占位符，需替换后执行。多架构发布使用 `--push`，不要依赖传统
Docker 本地镜像存储一次 `--load` 装入多个平台。

## 配置与持久化

以下命令是部署说明，不会由构建自动执行。已有配置时不要覆盖，先核对内容。

```bash
cp config.example.yaml config.yaml
mkdir -p data
# 仅调整专用数据目录及这份配置，不要递归修改媒体目录。
sudo chown 10001:10001 data config.yaml
sudo chmod 0750 data
sudo chmod 0600 config.yaml
```

修改配置后以只读方式挂载到 `/config/config.yaml`。至少核对：

```yaml
server:
  host: "0.0.0.0"
  port: 9091
database:
  file_path: "/app/data/proxy.db"
biglybt:
  rpc_url: "http://biglybt:9092/transmission/rpc"
  username: "REPLACE_WITH_BIGLYBT_USERNAME"
  password: "REPLACE_WITH_BIGLYBT_PASSWORD"
sonarr:
  base_url: "http://sonarr:8989"
  api_key: "REPLACE_WITH_SONARR_API_KEY"
  timeout_seconds: 10
path_rules:
  default_download_dir: "/storage/downloads"
  disk_mappings:
    - media_prefix: "/media/tv"
      download_dir: "/storage/downloads/tv"
  docker_mappings:
    - host_path: "/storage/downloads"
      container_path: "/downloads"
```

这是字段示例，不是真实凭据；将其合入模板并按实际网络与路径修改。
必须持久化整个 `/app/data` 目录，而不只是 `proxy.db`，因为 SQLite WAL
还会写入 `-wal`、`-shm` 文件。优先使用支持 SQLite 文件锁的本地文件系统。
备份时应先停止自己的代理或使用 SQLite 一致性备份，不能仅复制运行中的主库文件。

bind mount 会覆盖镜像内的目录权限。默认 UID/GID 10001 必须能读取配置、遍历
其父目录，并能写入数据目录；不要用 `chmod 777`。也可在 Compose 中显式指定
`user: "<UID>:<GID>"`，再按该身份准备权限，镜像不会处理 `PUID`/`PGID`
环境变量。rootless Docker/Podman 的宿主机 ID 可能经过映射，需按引擎的
user namespace 规则准备所有权（Podman 可使用 `podman unshare chown`）。
启用 SELinux 的宿主机还需为专用 bind mount 设置适当的容器标签；仅对这些
专用目录/文件使用 `:Z` 或等效标签操作，不要重标记现有媒体树。

## Compose 与网络

```bash
# 先检查合并配置，不会启动任何服务。
docker compose -f compose.example.yaml config
# 确认配置、目录权限和网络后，由部署者主动启动。
docker compose -f compose.example.yaml up -d --build
```

示例默认构建本地 `arr-download-proxy:local`；使用发布镜像时设置
`ARR_DOWNLOAD_PROXY_IMAGE=<registry>/<namespace>/arr-download-proxy:<version>`，
并使用 `docker compose -f compose.example.yaml up -d --no-build`。
配置缺失时挂载会失败，不会自动创建同名目录。日志输出到标准输出/错误，
无需挂载日志目录；按容器引擎的日志驱动配置轮转。

容器内 `127.0.0.1` 指向容器自身，不是宿主机或其他容器。上面的 `biglybt`
和 `sonarr` 只有在同一用户定义网络、且服务名/网络别名存在时才能解析。
实际部署可将本服务加入已有的受控外部网络，或使用能从容器访问的服务地址；
不要因此重建或重启已有 Sonarr/BiglyBT 服务。

端口默认绑定宿主机 `127.0.0.1:9091`。远程 Sonarr 无法直接访问它；如有需要，
将绑定地址改为受控的 LAN 地址并配置防火墙，或在同一容器网络直接访问
`http://arr-download-proxy:9091`。Sonarr 的 Transmission RPC 地址为
`/transmission/rpc`，Webhook 地址为 `/webhook/sonarr`。不要把服务直接暴露到
公网，尤其 Webhook 与 `/health` 不经过 RPC 鉴权。

路径映射是协议中的路径字符串转换，不是为代理挂载磁盘：`media_prefix`
匹配 Sonarr 报告的剧集路径，`download_dir` 必须是 BiglyBT 实际可写的路径；
`docker_mappings.host_path` 对应 BiglyBT 返回路径，`container_path` 对应
Sonarr 内能访问同一文件的位置。例如 BiglyBT 看到 `/storage/downloads/tv`、
Sonarr 看到 `/downloads/tv`，则使用上述规则。代理本身不搬运媒体，通常无需
挂载下载目录。Sonarr 与 BiglyBT 的卷布局仍需保证下载与媒体位于同一文件系统，
路径转换本身不能消除跨文件系统的硬链接限制。

## 安全的离线 smoke 检查

```bash
docker run --rm --name arr-download-proxy-help-check --network none \
  --read-only arr-download-proxy:local --help
```

`--help` 不加载配置、不打开数据库、不监听端口、不连接 Sonarr 或 BiglyBT。
不要使用实际数据卷做 smoke 检查：正常启动会恢复 SQLite 中未完成任务并向
BiglyBT 重放。镜像没有自动健康检查，以免探测引入额外流量或运行层工具依赖。
