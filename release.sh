#!/bin/bash
# Kanju 客户端发布脚本：打包产物并发布到 GitHub Releases。
#
# 用法:
#   ./release.sh                # 打印本帮助
#   ./release.sh upload         # 只上传 dist/releases 现有产物到新 Release
#   ./release.sh build          # 清理 + 构建（macos_arm64 + android_arm64）+ 上传
#   ./release.sh build --no-test  # 同上，跳过测试
#   ./release.sh 0.2.0 upload   # 指定版本号上传（默认取 nsgo.yaml 的 version）
#
# 上传规则:
#   - tag 形如 v<version>-<8位uuid>，每次发布都是新 Release（不覆盖历史，
#     测试阶段频繁发包可随时回退任意版本）
#   - install.sh / install.ps1 不上传
#
# 凭据来源（按顺序）:
#   1. GITHUB_TOKEN / GH_TOKEN 环境变量
#   2. git credential fill（osxkeychain 里存的 GitHub token）
set -euo pipefail

cd "$(dirname "$0")"

usage() {
    sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
}

MODE="${1:-}"
[ -z "$MODE" ] && usage
shift
case "$MODE" in
    -h|--help|"") usage ;;
    upload|build) ;;
    *) echo "未知参数: $MODE（可用: upload | build）" >&2; exit 1 ;;
esac

run_tests=true
while [ $# -gt 0 ]; do
    case "$1" in
        --no-test) run_tests=false ;;
        *) VERSION="$1" ;;  # 版本号可放在任意位置
    esac
    shift
done

# 版本号：参数 > nsgo.yaml。
if [ -z "${VERSION:-}" ]; then
    VERSION=$(sed -n 's/^version: *//p' nsgo.yaml | head -1)
fi
[ -z "$VERSION" ] && { echo "✗ 未指定版本且 nsgo.yaml 无 version"; exit 1; }
# tag 追加 8 位随机 uuid（测试阶段频繁发包，同版本号每次发都是新的
# Release，不覆盖历史；uuid 短到肉眼可辨）。
UUID=$(python3 -c "import uuid; print(uuid.uuid4().hex[:8])")
TAG="v${VERSION}-${UUID}"

build_pkgs() {
    echo "==> 清理构建缓存（go 编译/测试缓存 + dist 旧产物）"
    go clean -cache -testcache
    rm -rf dist/*

    if [ -f .env ]; then
        echo "==> 加载本机配置（.env）"
        set -a; source .env; set +a
    fi

    if $run_tests; then
        echo "==> go vet"
        go vet ./...
        echo "==> headless UI 测试"
        # nsgo dispose unlink 竞态（docs/nsgo-bugs.md #7）偶发 panic 使
        # 整批测试退出，重跑即过——上游修复前自动重试一次。
        if ! go tool nsgo test -target macos .; then
            echo "==> 测试偶发失败（框架 dispose 竞态 #7？），自动重跑一次"
            go tool nsgo test -target macos .
        fi
    fi

    echo "==> 构建 macos_arm64 + android_arm64"
    go tool nsgo build -target macos_arm64,android_arm64
    echo "==> 构建完成，产物:"
    ls -la dist/releases/ | grep -v install
}

upload_pkgs() {
    # 仓库：release.github > git remote origin。
    REPO=$(sed -n 's/.*github: *\([A-Za-z0-9_.-]*\/[A-Za-z0-9_.-]*\).*/\1/p' nsgo.yaml | head -1)
    if [ -z "$REPO" ]; then
        URL=$(git remote get-url origin)
        REPO=$(echo "$URL" | sed -E 's#.*github.com[:/](.+)(\.git)?$#\1#; s#\.git$##')
    fi
    [ -z "$REPO" ] && { echo "✗ 无法确定 GitHub 仓库"; exit 1; }

    # Token：环境变量 > git credential。
    TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
    if [ -z "$TOKEN" ]; then
        TOKEN=$(printf 'protocol=https\nhost=github.com\n' | git credential fill 2>/dev/null | sed -n 's/^password=//p')
    fi
    [ -z "$TOKEN" ] && { echo "✗ 无 GitHub token（设置 GITHUB_TOKEN 或 git credential）"; exit 1; }
    AUTH="Authorization: Bearer $TOKEN"

    # 产物：排除 install.sh/install.ps1（用户要求不发布安装脚本；
    # GitHub 自动带的源码 zip/tar.gz 无法去除，从产物侧不传这些）。
    FILES=()
    for f in dist/releases/*.*; do
        case "$(basename "$f")" in
            install.sh|install.ps1) continue ;;
            *) FILES+=("$f") ;;
        esac
    done
    [ ${#FILES[@]} -gt 0 ] || { echo "✗ dist/releases 无产物（build 模式先构建，upload 模式检查 dist/releases）"; exit 1; }
    echo "==> 仓库 $REPO  tag $TAG  产物 ${#FILES[@]} 个"
    printf "    %s\n" "${FILES[@]##*/}"

    api() { curl -sf -H "$AUTH" -H "Accept: application/vnd.github+json" "$@"; }

    # Release 是否已存在（正常不会，tag 含 uuid；防御性保留）。
    if api "https://api.github.com/repos/$REPO/releases/tags/$TAG" > /dev/null 2>&1; then
        echo "==> Release $TAG 已存在，追加资产"
        RELEASE_ID=$(api "https://api.github.com/repos/$REPO/releases/tags/$TAG" | sed -n 's/.*"id": \([0-9]*\),.*/\1/p' | head -1)
    else
        echo "==> 创建 Release $TAG"
        BODY=$(jq -n --arg tag "$TAG" --arg name "Kanju $VERSION ($UUID)" \
            '{tag_name: $tag, name: $name, body: "Kanju 客户端构建"}' 2>/dev/null \
            || printf '{"tag_name":"%s","name":"Kanju %s (%s)","body":"Kanju 客户端构建"}' "$TAG" "$VERSION" "$UUID")
        RELEASE_ID=$(api -X POST -d "$BODY" "https://api.github.com/repos/$REPO/releases" | sed -n 's/.*"id": \([0-9]*\),.*/\1/p' | head -1)
    fi
    [ -n "$RELEASE_ID" ] || { echo "✗ 创建/查询 Release 失败"; exit 1; }

    UPLOAD_URL="https://uploads.github.com/repos/$REPO/releases/$RELEASE_ID/assets?name="
    for f in "${FILES[@]}"; do
        name=$(basename "$f")
        # 删同名旧资产（防御，tag 含 uuid 时正常不存在）。
        for id in $(api "https://api.github.com/repos/$REPO/releases/$RELEASE_ID/assets" 2>/dev/null \
            | python3 -c "import json,sys;[print(a['id']) for a in json.load(sys.stdin) if a['name']=='$name']" 2>/dev/null); do
            api -X DELETE "https://api.github.com/repos/$REPO/releases/assets/$id" >/dev/null
            echo "    已删旧资产 $name"
        done
        echo "==> 上传 $name"
        curl -sf -X POST -H "$AUTH" -H "Content-Type: application/octet-stream" \
            --data-binary "@$f" "${UPLOAD_URL}${name}" > /dev/null \
            && echo "    ✓ $name" || { echo "    ✗ $name 上传失败"; exit 1; }
    done

    echo ""
    echo "==> 完成。下载页: https://github.com/$REPO/releases/tag/$TAG"
    echo "    最新版直链: https://github.com/$REPO/releases/latest"
}

case "$MODE" in
    build)
        build_pkgs
        upload_pkgs
        ;;
    upload)
        upload_pkgs
        ;;
esac
