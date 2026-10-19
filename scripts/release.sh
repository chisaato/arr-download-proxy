#!/usr/bin/env bash
set -euo pipefail

# 本地打包不依赖 Git；校验入口也供标签发布工作流使用。
validate_version() {
  local version=$1 identifier prerelease
  local number='(0|[1-9][0-9]*)'
  local identifier_pattern='[0-9A-Za-z-]+'
  local pattern="^v${number}\\.${number}\\.${number}(-${identifier_pattern}(\\.${identifier_pattern})*)?(\\+${identifier_pattern}(\\.${identifier_pattern})*)?$"
  if [[ ! $version =~ $pattern ]]; then
    printf '版本必须是 vSemVer，例如 v1.2.3 或 v1.2.3-rc.1\n' >&2
    return 1
  fi
  prerelease=${version%%+*}
  if [[ $prerelease == *-* ]]; then
    prerelease=${prerelease#*-}
    local -a identifiers
    IFS='.' read -r -a identifiers <<< "$prerelease"
    for identifier in "${identifiers[@]}"; do
      if [[ $identifier =~ ^[0-9]+$ && $identifier == 0?* ]]; then
        printf 'SemVer 预发布数字标识不能含前导零\n' >&2
        return 1
      fi
    done
    printf 'prerelease\n'
  else
    printf 'stable\n'
  fi
}

if [[ ${1:-} == --validate-version && $# == 2 ]]; then
  validate_version "$2"
  exit
fi
if [[ $# -lt 1 || $# -gt 2 ]]; then
  printf '用法: %s VERSION [OUTPUT_DIR]\n       %s --validate-version VERSION\n' "$0" "$0" >&2
  exit 2
fi
version=$1
validate_version "$version" >/dev/null
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output=${2:-$(mktemp -d "${TMPDIR:-/tmp}/arr-download-proxy-release.XXXXXX")}
mkdir -p -- "$output"
output=$(cd -- "$output" && pwd)
for asset in "arr-download-proxy_${version}_linux_amd64.tar.gz" "arr-download-proxy_${version}_linux_arm64.tar.gz" SHA256SUMS; do
  if [[ -e "$output/$asset" ]]; then
    printf '拒绝覆盖已有产物: %s\n' "$output/$asset" >&2
    exit 1
  fi
done
stage=$(mktemp -d "${TMPDIR:-/tmp}/arr-download-proxy-stage.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
cd -- "$root"
if [[ ! -f LICENSE ]]; then
  printf '警告: 仓库没有 LICENSE；归档不附带许可证，请维护者明确授权协议。\n' >&2
fi
for arch in amd64 arm64; do
  name="arr-download-proxy_${version}_linux_${arch}"
  mkdir -- "$stage/$name"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags='-s -w' -o "$stage/$name/arr-download-proxy" ./cmd/server
  cp -- config.example.yaml "$stage/$name/"
  if [[ -f LICENSE ]]; then
    cp -- LICENSE "$stage/$name/"
  fi
  tar -C "$stage" -czf "$output/$name.tar.gz" "$name"
done
(
  cd -- "$output"
  sha256sum "arr-download-proxy_${version}_linux_amd64.tar.gz" "arr-download-proxy_${version}_linux_arm64.tar.gz" > SHA256SUMS
)
printf '产物目录: %s\n' "$output"
