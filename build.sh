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
#   release      = ./release.sh build：清理 + 构建（macos_arm64 + android_arm64）+ 上传 GitHub Releases
#   all          全部平台
#   webapp       wasm UI 站点 + kanju-web 宿主（自托管：单二进制 = 后端 + Web 前端）
#   macos_arm64 / macos_x64 / windows_x64 / windows_arm64
#   linux_x64 / linux_arm64 / ios / ios_simulator
#   android_arm64 / android_x64 / web（仅 wasm 站点，无后端宿主）
#
# 选项:
#   --no-test    跳过 vet 与测试，直接构建
#   其余参数原样透传给 nsgo build（如 -open、-version）
#
# 产物: dist/ 与 dist/releases/
set -euo pipefail

usage() {
    sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
}

[ $# -eq 0 ] && usage

cd "$(dirname "$0")"

run_tests=true
do_run=false
build_webapp=false
args=()
for arg in "$@"; do
    case "$arg" in
        -h|--help) usage ;;
        --no-test) run_tests=false ;;
        run) do_run=true ;;
        macos) args+=(-target macos_arm64) ;;
        webapp) build_webapp=true ;;
        all|macos_arm64|macos_x64|windows_x64|windows_arm64|linux_x64|linux_arm64|ios|ios_simulator|android_arm64|android_x64|web)
            args+=(-target "$arg") ;;
        *) args+=("$arg") ;;
    esac
done

# run 未显式给 target 时默认本机 macOS。
if $do_run && [[ " ${args[*]-} " != *" -target "* ]]; then
    args=(-target macos_arm64 "${args[@]-}")
fi

echo "==> 加载本机配置（.env）"
if [ -f .env ]; then
    set -a
    source .env
    set +a
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
    # nsgo dispose unlink 竞态（docs/nsgo-bugs.md #7）偶发 panic 使
    # 整批测试退出，重跑即过——上游修复前自动重试一次。
    if ! go tool nsgo test -target macos .; then
        echo "==> 测试偶发失败（框架 dispose 竞态 #7？），自动重跑一次"
        go tool nsgo test -target macos .
    fi
fi

if $do_run; then
    echo "==> nsgo build -run ${args[*]}"
    go tool nsgo build -run "${args[@]}"
else
    if $build_webapp; then
        echo "==> 构建 Web 应用（wasm UI 站点 + kanju-web 宿主）"
        site_tmp=$(mktemp -d)
        trap 'rm -rf "$site_tmp"' EXIT
        go tool nsgo build -target web -output "$site_tmp"
        site_dir=$(find "$site_tmp" -maxdepth 1 -type d -name 'Kanju-*' | head -1)
        rm -rf cmd/kanju-web/web/dist
        mkdir -p cmd/kanju-web/web/dist
        cp -R "$site_dir/." cmd/kanju-web/web/dist/
        go build -ldflags "-s -w" -o dist/kanju-web ./cmd/kanju-web/
        echo "==> 完成: dist/kanju-web（运行: dist/kanju-web [-port 28088] [-db path]）"
    else
        echo "==> nsgo build ${args[*]}"
        go tool nsgo build "${args[@]}"
        echo "==> 完成，产物在 dist/"
        ls -d dist/*/ 2>/dev/null | tail -5
    fi
fi
