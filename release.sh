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
#   - 固定 tag "latest"：每次发版替换旧 Release（不累积 tag/历史 Release），
#     tag 指向最新 commit；/releases/latest 下载链接不变）
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
# 固定单一 tag "latest"：每次发版删旧 Release + 移动 tag 到最新 commit
# 再重建——正式 Release 必须有 tag（GitHub API 强制），这是不累积 tag
# 的唯一方式。用户侧 /releases/latest 下载页不变。
TAG="latest"

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
        # 整批测试退出，重跑即过——上游修复前自动重试，最多 3 次
        # （机器负载高时偶发率上升，1 次不够）。
        ok=false
        for i in 1 2 3; do
            if go tool nsgo test -target macos .; then
                ok=true
                break
            fi
            [ $i -lt 3 ] && echo "==> 测试偶发失败（框架 dispose 竞态 #7？），重跑第 $((i+1))/3 次"
        done
        $ok || { echo "✗ 测试连续 3 次失败"; exit 1; }
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

    # 固定 tag 发版：旧 Release 连同 tag 一起删，重建到当前 commit。
    if OLD_ID=$(api "https://api.github.com/repos/$REPO/releases/tags/$TAG" 2>/dev/null | sed -n 's/.*"id": \([0-9]*\),.*/\1/p' | head -1) && [ -n "$OLD_ID" ]; then
        echo "==> 删除旧 Release $TAG (id=$OLD_ID)"
        api -X DELETE "https://api.github.com/repos/$REPO/releases/$OLD_ID" 2>/dev/null || true
        curl -s -X DELETE -H "$AUTH" "https://api.github.com/repos/$REPO/git/refs/tags/$TAG" -o /dev/null
    fi
    echo "==> 创建 Release $TAG（指向最新 commit）"
    BODY=$(jq -n --arg tag "$TAG" --arg name "Kanju $VERSION" --arg sha "$(git rev-parse HEAD)" \
        '{tag_name: $tag, name: $name, body: "Kanju 客户端构建", target_commitish: $sha}' 2>/dev/null \
        || printf '{"tag_name":"%s","name":"Kanju %s","body":"Kanju 客户端构建","target_commitish":"%s"}' "$TAG" "$VERSION" "$(git rev-parse HEAD)")
    RELEASE_ID=$(api -X POST -d "$BODY" "https://api.github.com/repos/$REPO/releases" | sed -n 's/.*"id": \([0-9]*\),.*/\1/p' | head -1)
    [ -n "$RELEASE_ID" ] || { echo "✗ 创建 Release 失败"; exit 1; }

    UPLOAD_URL="https://uploads.github.com/repos/$REPO/releases/$RELEASE_ID/assets?name="
    for f in "${FILES[@]}"; do
        name=$(basename "$f")
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
