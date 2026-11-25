#!/bin/bash
# Agent24 fork product patch A24-KB-61 (KB-LITE-01b): build a headless Lite artifact from an exact source SHA.
set -euo pipefail

die() {
    echo "ERROR: $*" >&2
    exit 1
}

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd) || die "cannot locate script directory"
repo_root=$(cd "${script_dir}/../.." && pwd) || die "cannot locate repository root"

# shellcheck source=scripts/agent24/lite-manifest-lib.sh
. "${script_dir}/lite-manifest-lib.sh" || die "cannot source lite-manifest-lib.sh"

work_tmp=""
src_wt=""

cleanup() {
    status=$?
    if [ -n "${src_wt}" ] && [ -d "${src_wt}" ]; then
        if ! git -C "$repo_root" worktree remove --force "$src_wt"; then
            echo "ERROR: git worktree remove failed" >&2
            if [ "$status" -eq 0 ]; then
                status=1
            fi
        fi
    fi
    if [ -n "${work_tmp}" ] && [ -d "${work_tmp}" ]; then
        if ! rm -rf "$work_tmp"; then
            echo "ERROR: temp cleanup failed" >&2
            if [ "$status" -eq 0 ]; then
                status=1
            fi
        fi
    fi
    exit "$status"
}
trap cleanup EXIT

if [ "$#" -lt 1 ]; then
    die "usage: build-lite-headless.sh <40-hex-sha> --out <dir> (--sign-key <key> | --unsigned)"
fi

sha=$1
shift

out=""
sign_key=""
unsigned=0
have_sign_key=0

while [ "$#" -gt 0 ]; do
    case "$1" in
        --out)
            if [ "$#" -lt 2 ]; then
                die "--out requires a directory"
            fi
            out=$2
            shift 2
            ;;
        --sign-key)
            if [ "$#" -lt 2 ]; then
                die "--sign-key requires a path"
            fi
            sign_key=$2
            have_sign_key=1
            shift 2
            ;;
        --unsigned)
            unsigned=1
            shift
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
done

if ! printf '%s' "$sha" | grep -E '^[0-9a-f]{40}$' >/dev/null; then
    die "source SHA must be exactly 40 lowercase hex characters"
fi
if [ -z "$out" ]; then
    die "--out is required"
fi
if [ "$unsigned" -eq 1 ] && [ "$have_sign_key" -eq 1 ]; then
    die "pass exactly one of --sign-key or --unsigned"
fi
if [ "$unsigned" -eq 0 ] && [ "$have_sign_key" -eq 0 ]; then
    die "pass exactly one of --sign-key or --unsigned"
fi
if [ "$have_sign_key" -eq 1 ] && [ ! -f "$sign_key" ]; then
    die "sign key not found: ${sign_key}"
fi

for cmd in git go tar shasum; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        die "required command not found: ${cmd}"
    fi
done
if [ "$have_sign_key" -eq 1 ]; then
    if ! command -v ssh-keygen >/dev/null 2>&1; then
        die "required command not found: ssh-keygen"
    fi
fi

if ! mkdir -p "$out"; then
    die "cannot create output directory: ${out}"
fi
out=$(cd "$out" && pwd) || die "cannot resolve output directory"
if [ "$have_sign_key" -eq 1 ]; then
    sign_key_dir=$(cd "$(dirname "$sign_key")" && pwd) || die "cannot resolve sign key directory"
    sign_key="${sign_key_dir}/$(basename "$sign_key")"
fi

if ! resolved=$(git -C "$repo_root" rev-parse --verify "${sha}^{commit}"); then
    die "git rev-parse failed for ${sha}"
fi
if [ "$resolved" != "$sha" ]; then
    die "resolved commit ${resolved} does not match ${sha}"
fi

work_tmp=$(mktemp -d "${TMPDIR:-/tmp}/lite-headless.XXXXXX") || die "mktemp failed"
src_wt="${work_tmp}/src"
# git worktree add prints "Preparing worktree" on stdout. The success
# contract is only the ARCHIVE/MANIFEST/SIG lines.
if ! git -C "$repo_root" worktree add --detach "$src_wt" "$sha" >&2; then
    die "git worktree add failed for ${sha}"
fi

if ! head_now=$(git -C "$src_wt" rev-parse HEAD); then
    die "git rev-parse HEAD failed"
fi
if [ "$head_now" != "$sha" ]; then
    die "worktree HEAD ${head_now} does not match ${sha}"
fi

cd "$src_wt" || die "cannot enter source worktree"

if ! GOOS=$(go env GOOS); then
    die "go env GOOS failed"
fi
if ! GOARCH=$(go env GOARCH); then
    die "go env GOARCH failed"
fi
export CGO_ENABLED=1
export CGO_CFLAGS="-Wno-deprecated-declarations"
if [ "$GOOS" = "darwin" ]; then
    export CGO_LDFLAGS="-Wl,-no_warn_duplicate_libraries"
fi

if ! VERSION=$(cat VERSION); then
    die "cannot read VERSION"
fi
if ! commit_time=$(git show -s --format=%cI "$sha"); then
    die "git show commit time failed"
fi
if ! GOVERSION=$(go env GOVERSION); then
    die "go env GOVERSION failed"
fi

short12=$(printf '%s' "$sha" | cut -c1-12)
stage_name="WeKnora-lite-headless_${VERSION}_${short12}_${GOOS}_${GOARCH}"
stage_dir="${work_tmp}/${stage_name}"
mkdir -p "$stage_dir" || die "cannot create stage directory"

ldflags="-w -s -X 'github.com/Tencent/WeKnora/internal/handler.Version=${VERSION}' -X 'github.com/Tencent/WeKnora/internal/handler.Edition=lite' -X 'github.com/Tencent/WeKnora/internal/handler.CommitID=${sha}' -X 'github.com/Tencent/WeKnora/internal/handler.BuildTime=${commit_time}' -X 'github.com/Tencent/WeKnora/internal/handler.GoVersion=${GOVERSION}'"

if ! go build -trimpath -buildvcs=false -tags sqlite_fts5 -ldflags "${ldflags}" -o "${stage_dir}/WeKnora-lite" ./cmd/server; then
    die "go build failed"
fi

# go-m1cpu v0.1.x (reached via milvus -> gopsutil/v3/cpu) runs cgo in package init
# and segfaults on Apple M5 before main starts; v0.2.x initializes lazily and knows M5.
if ! deps=$(go version -m "${stage_dir}/WeKnora-lite"); then
    die "go version -m failed"
fi
if printf '%s\n' "$deps" | grep -E 'github.com/shoenig/go-m1cpu[[:space:]]+v0\.1\.' >/dev/null; then
    die "binary links go-m1cpu v0.1.x, which crashes at startup on Apple M5; need v0.2.4 or later"
fi

# gojieba loads its dictionary at process start. Ship it next to the binary;
# internal/types finds <exe dir>/dict without JIEBA_DICT_DIR.
if ! jieba_dir=$(go list -m -f '{{.Dir}}' github.com/yanyiwu/gojieba); then
    die "go list gojieba failed"
fi
if [ ! -f "${jieba_dir}/deps/cppjieba/dict/jieba.dict.utf8" ]; then
    die "gojieba dictionary not found under ${jieba_dir}"
fi
if ! cp -R "${jieba_dir}/deps/cppjieba/dict" "${stage_dir}/dict"; then
    die "copy jieba dictionary failed"
fi
chmod -R u+w "${stage_dir}/dict" || die "chmod jieba dictionary failed"

# Run from an unrelated directory with JIEBA_DICT_DIR unset, so the check
# proves the artifact starts on its own bundled files.
if ! version_out=$(cd "$work_tmp" && env -u JIEBA_DICT_DIR "${stage_dir}/WeKnora-lite" --version); then
    die "WeKnora-lite --version failed"
fi
if ! printf '%s\n' "$version_out" | grep -F "commit ${sha}" >/dev/null; then
    die "--version output missing commit ${sha}"
fi
if ! printf '%s\n' "$version_out" | grep -F "edition lite" >/dev/null; then
    die "--version output missing edition lite"
fi

if ! bash scripts/copy-licenses.sh "$stage_dir"; then
    die "copy-licenses failed"
fi
if ! cp -R config "${stage_dir}/config"; then
    die "copy config failed"
fi
if ! mkdir -p "${stage_dir}/migrations"; then
    die "cannot create migrations directory"
fi
if ! cp -R migrations/sqlite "${stage_dir}/migrations/"; then
    die "copy migrations failed"
fi
if ! cp .env.lite.example "${stage_dir}/"; then
    die "copy .env.lite.example failed"
fi
if ! cp docs/LITE.md "${stage_dir}/README.md"; then
    die "copy README failed"
fi

archive_name="${stage_name}.tar.gz"
archive_path="${out}/${archive_name}"
if ! tar -czf "$archive_path" -C "$work_tmp" "$stage_name"; then
    die "tar failed"
fi

if ! archive_sha256=$(shasum -a 256 "$archive_path" | awk '{print $1}'); then
    die "shasum archive failed"
fi
if ! binary_sha256=$(shasum -a 256 "${stage_dir}/WeKnora-lite" | awk '{print $1}'); then
    die "shasum binary failed"
fi
if ! license_sha256=$(shasum -a 256 "${stage_dir}/LICENSE" | awk '{print $1}'); then
    die "shasum LICENSE failed"
fi
if ! notices_sha256=$(shasum -a 256 "${stage_dir}/THIRD_PARTY_NOTICES.md" | awk '{print $1}'); then
    die "shasum THIRD_PARTY_NOTICES.md failed"
fi
if [ -z "$archive_sha256" ] || [ -z "$binary_sha256" ] || [ -z "$license_sha256" ] || [ -z "$notices_sha256" ]; then
    die "empty sha256"
fi

license_line=$(sed -n '1{s/^[[:space:]]*//;s/[[:space:]]*$//p;}' "${stage_dir}/LICENSE") || die "cannot read LICENSE"

manifest_path="${out}/${stage_name}.manifest.json"
if [ "$unsigned" -eq 1 ]; then
    signed_value=false
else
    signed_value=true
fi

write_lite_manifest "$manifest_path" "$sha" "$VERSION" "$GOOS" "$GOARCH" "$GOVERSION" "$commit_time" \
    "$archive_name" "$archive_sha256" "$binary_sha256" "$license_line" "$license_sha256" "$notices_sha256" \
    "$signed_value"

if [ "$unsigned" -eq 1 ]; then
    echo "WARNING: unsigned manifest" >&2
    sig_path=""
else
    sign_lite_manifest "$sign_key" "$manifest_path"
    if [ ! -f "${manifest_path}.sig" ]; then
        die "signature file missing"
    fi
    if ! pub=$(ssh-keygen -y -f "$sign_key"); then
        die "ssh-keygen -y failed"
    fi
    pub_two=$(printf '%s\n' "$pub" | cut -d' ' -f1,2)
    printf 'agent24-lite %s\n' "$pub_two" > "${work_tmp}/allowed_signers"
    verify_out="${work_tmp}/verify.out"
    if ! bash "${script_dir}/verify-lite-headless.sh" "$manifest_path" "$archive_path" "${work_tmp}/allowed_signers" "$sha" >"$verify_out"; then
        die "signed manifest failed verification"
    fi
    if ! grep -F "OK ${sha} ${GOOS}/${GOARCH}" "$verify_out" >/dev/null; then
        die "signed manifest verification output mismatch"
    fi
    sig_path="${manifest_path}.sig"
fi

printf 'ARCHIVE=%s\n' "$archive_path"
printf 'MANIFEST=%s\n' "$manifest_path"
printf 'SIG=%s\n' "$sig_path"
