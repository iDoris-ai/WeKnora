#!/usr/bin/env bash
# 按受测 HEAD 构建 app 镜像（docker/Dockerfile.app，WITH_ANYDOC=1）。
# 不碰运行中的栈，不写 compose 覆盖文件，不起栈。说明见 README 第 11 节。
# 用法：scripts/agent24/probe/build-local-app.sh
# 环境变量：STACK_DIR（默认 $HOME/weknora-probe-stack）。
set -uo pipefail
PROBE_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$PROBE_DIR/../../.." && pwd)
STACK_DIR=${STACK_DIR:-$HOME/weknora-probe-stack}
BASELINE=$ROOT/docs/agent24/UPSTREAM-BASELINE.json
# shellcheck source=patches.sh
. "$PROBE_DIR/patches.sh" || { echo "前置核对失败：加载 ${PROBE_DIR}/patches.sh 失败" >&2; exit 3; }

command -v jq >/dev/null || { echo "前置核对失败：缺少 jq" >&2; exit 3; }
command -v docker >/dev/null || { echo "前置核对失败：缺少 docker" >&2; exit 3; }
REL_TAG=$(jq -er '.current_baseline.release.tag' "$BASELINE" 2>/dev/null) \
  || { echo "前置核对失败：读不到 ${BASELINE} 的 .current_baseline.release.tag" >&2; exit 3; }
[[ $REL_TAG =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
  || { echo "前置核对失败：登记的 release.tag 无效（${REL_TAG}），应形如 v0.8.3" >&2; exit 3; }
REL_COMMIT=$(jq -er '.current_baseline.release.commit' "$BASELINE" 2>/dev/null) \
  || { echo "前置核对失败：读不到 ${BASELINE} 的 .current_baseline.release.commit" >&2; exit 3; }
REV=$(git -C "$ROOT" rev-parse HEAD) || { echo "前置核对失败：读不到 HEAD" >&2; exit 3; }
SHA8=$(git -C "$ROOT" rev-parse --short=8 HEAD) || { echo "前置核对失败：读不到 HEAD" >&2; exit 3; }

probe_require_clean_worktree || exit 3
probe_namespace_diff || exit 3
[ -d "$STACK_DIR" ] || { echo "失败：STACK_DIR 不是目录：${STACK_DIR}" >&2; exit 1; }

VERSION_ARG="${REL_TAG#v}+agent24.${SHA8}"
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
TAG="weknora-app:agent24-${SHA8}"

# 不传 GO_VERSION_ARG：get_version.sh 取的是宿主机的 go version，不是镜像里实际编译用的 Go，传了反而误导。
docker_build=(docker build -f docker/Dockerfile.app
  --build-arg WITH_ANYDOC=1
  --build-arg "VERSION_ARG=${VERSION_ARG}"
  --build-arg "COMMIT_ID_ARG=${REV}"
  --build-arg "BUILD_TIME_ARG=${BUILD_TIME}")
docker_build+=(--label "org.opencontainers.image.revision=${REV}"
  --label "io.idoris.agent24.base-release=${REL_TAG}"
  -t "$TAG"
  "$ROOT")
( cd "$ROOT" && "${docker_build[@]}" ) || { echo "构建失败" >&2; exit 1; }

IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$TAG") || { echo "失败：读不到镜像 ${TAG} 的 Id" >&2; exit 1; }
[ -n "$IMAGE_ID" ] || { echo "失败：镜像 ${TAG} 的 Id 为空" >&2; exit 1; }
patch_ids=$(jq -c '.current_baseline.product.product_patches | if type == "array" then [.[] | .id] else [] end' "$BASELINE") \
  || { echo "失败：读不到 product_patches" >&2; exit 1; }
jq -n \
  --arg revision "$REV" \
  --arg image_id "$IMAGE_ID" \
  --arg tag "$TAG" \
  --arg version "$VERSION_ARG" \
  --arg base_release "$REL_TAG" \
  --argjson patch_ids "$patch_ids" \
  --arg built_at_utc "$BUILD_TIME" \
  --arg with_anydoc "1" \
  --arg version_arg "$VERSION_ARG" \
  --arg commit_id_arg "$REV" \
  --arg build_time_arg "$BUILD_TIME" \
  '{revision: $revision, image_id: $image_id, tag: $tag, version: $version,
    base_release: $base_release, patch_ids: $patch_ids, built_at_utc: $built_at_utc,
    build_args: {WITH_ANYDOC: $with_anydoc, VERSION_ARG: $version_arg,
      COMMIT_ID_ARG: $commit_id_arg, BUILD_TIME_ARG: $build_time_arg}}' \
  >"$STACK_DIR/agent24-local-app.json" || { echo "失败：写不了 ${STACK_DIR}/agent24-local-app.json" >&2; exit 1; }

cat <<EOF
已写入 ${STACK_DIR}/agent24-local-app.json
下一步：在 ${STACK_DIR} 用一个 compose 覆盖文件把 app 的 image 换成 ${TAG}，再 up -d。
本脚本不写覆盖文件、不起栈。不要把覆盖文件命名为 docker-compose.override.yml 或 compose.override.yml（compose 会自动合并；登记的约束不允许项目目录里有这类文件）。覆盖文件内容：

services:
  app:
    image: ${TAG}

在 ${STACK_DIR} 自行保存为覆盖文件后执行（示例）：
env -u WEKNORA_VERSION -u COMPOSE_FILE -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE docker compose -f docker-compose.yml -f agent24-local-app.override.yml up -d --no-build
EOF
