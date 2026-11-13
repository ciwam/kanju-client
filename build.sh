#!/bin/bash
# Kanju 客户端构建脚本。
#
# 用法:
#   ./build.sh                  # 构建 macOS (arm64) + 测试
#   ./build.sh --no-test        # 跳过检查直接构建
#   ./build.sh -target all      # 透传 nsgo build 参数，构建全部平台
#   ./build.sh -target macos_arm64,windows_x64 -open
#
# 常用 target（go tool nsgo doctor 可看本机支持情况）:
#   macos_arm64 / macos_x64 / windows_x64 / windows_arm64
#   linux_x64 / linux_arm64 / ios / android_arm64 / web
#
# 产物: dist/ 与 dist/releases/
set -euo pipefail

cd "$(dirname "$0")"

run_tests=true
args=()
for arg in "$@"; do
    case "$arg" in
        --no-test) run_tests=false ;;
        *) args+=("$arg") ;;
    esac
done

echo "==> 检查环境"
if ! command -v go >/dev/null 2>&1; then
    echo "错误: 未找到 go" >&2
    exit 1
fi
if ! go tool nsgo build -h >/dev/null 2>&1; then
    echo "错误: nsgo 工具不可用，先执行: go get -tool github.com/nsgo-dev/nsgo/cmd/nsgo" >&2
    exit 1
fi

if $run_tests; then
    echo "==> go vet"
    go vet ./...

    echo "==> 后端 API 冒烟测试（后端未运行则自动跳过）"
    go test ./api/ -run TestLive -count=1

    echo "==> headless UI 测试"
    go tool nsgo test -target macos .
fi

echo "==> nsgo build ${args[*]:-（默认 target）}"
go tool nsgo build "${args[@]:-}"

echo "==> 完成，产物在 dist/"
ls -d dist/*/ 2>/dev/null | tail -5
