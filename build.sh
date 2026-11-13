#!/bin/bash
# Kanju 客户端构建脚本。
#
# 用法:
#   ./build.sh <target...> [选项]
#   ./build.sh              # 打印本帮助
#   ./build.sh macos        # 清理 + 构建 macOS (arm64) + 测试
#   ./build.sh macos --no-test
#   ./build.sh run          # 清理 + 构建 + 运行（构建完启动应用）
#   ./build.sh run --no-test
#   ./build.sh all          # 全部平台
#   ./build.sh macos,windows_x64 --open
#
# target（go tool nsgo doctor 可看本机支持情况）:
#   run          清理 + 构建 + 运行（本机平台）
#   macos        macos_arm64 的别名
#   all          全部平台
#   macos_arm64 / macos_x64 / windows_x64 / windows_arm64
#   linux_x64 / linux_arm64 / ios / ios_simulator
#   android_arm64 / android_x64 / web
#
# 选项:
#   --no-test    跳过 vet 与测试，直接构建
#   其余参数原样透传给 nsgo build（如 -open、-version）
#
# 产物: dist/ 与 dist/releases/
set -euo pipefail

usage() {
    sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
}

[ $# -eq 0 ] && usage

cd "$(dirname "$0")"

run_tests=true
do_run=false
args=()
for arg in "$@"; do
    case "$arg" in
        -h|--help) usage ;;
        --no-test) run_tests=false ;;
        run) do_run=true ;;
        macos) args+=(-target macos_arm64) ;;
        all|macos_arm64|macos_x64|windows_x64|windows_arm64|linux_x64|linux_arm64|ios|ios_simulator|android_arm64|android_x64|web)
            args+=(-target "$arg") ;;
        *) args+=("$arg") ;;
    esac
done

# run 未显式给 target 时默认本机 macOS。
if $do_run && [[ " ${args[*]-} " != *" -target "* ]]; then
    args=(-target macos_arm64 "${args[@]-}")
fi

echo "==> 检查环境"
if ! command -v go >/dev/null 2>&1; then
    echo "错误: 未找到 go" >&2
    exit 1
fi
if ! go tool nsgo build -h >/dev/null 2>&1; then
    echo "错误: nsgo 工具不可用，先执行: go get -tool github.com/nsgo-dev/nsgo/cmd/nsgo" >&2
    exit 1
fi

echo "==> 清理构建缓存（go 编译/测试缓存 + dist 旧产物）"
go clean -cache -testcache
rm -rf dist/*

if $run_tests; then
    echo "==> go vet"
    go vet ./...

    echo "==> 后端 API 冒烟测试（后端未运行则自动跳过）"
    go test ./api/ -run TestLive -count=1

    echo "==> headless UI 测试"
    go tool nsgo test -target macos .
fi

if $do_run; then
    echo "==> nsgo build -run ${args[*]}"
    go tool nsgo build -run "${args[@]}"
else
    echo "==> nsgo build ${args[*]}"
    go tool nsgo build "${args[@]}"
    echo "==> 完成，产物在 dist/"
    ls -d dist/*/ 2>/dev/null | tail -5
fi
