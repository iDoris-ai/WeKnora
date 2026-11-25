#!/bin/bash
# Agent24 fork product patch A24-KB-61 (KB-LITE-01b): fast checks for the headless Lite manifest without go build.
set -euo pipefail

die() {
    echo "ERROR: $*" >&2
    exit 1
}

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd) || die "cannot locate script directory"
repo_root=$(cd "${script_dir}/../.." && pwd) || die "cannot locate repository root"
verify="${script_dir}/verify-lite-headless.sh"
build="${script_dir}/build-lite-headless.sh"

# shellcheck source=scripts/agent24/lite-manifest-lib.sh
. "${script_dir}/lite-manifest-lib.sh" || die "cannot source lite-manifest-lib.sh"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/lite-headless-test.XXXXXX") || die "mktemp failed"
cleanup() {
    status=$?
    if [ -n "${tmp}" ] && [ -d "${tmp}" ]; then
        rm -rf "${tmp}"
    fi
    exit "$status"
}
trap cleanup EXIT

n=0
fail=0

pass() {
    n=$((n + 1))
    printf 'ok %s - %s\n' "$n" "$1"
}

fail_case() {
    n=$((n + 1))
    fail=$((fail + 1))
    printf 'not ok %s - %s\n' "$n" "$1"
}

ssh-keygen -t ed25519 -N '' -f "${tmp}/key" >/dev/null

source_sha="0123456789abcdef0123456789abcdef01234567"
stage_name="WeKnora-lite-headless_0.0.0_0123456789ab_darwin_arm64"
archive_name="${stage_name}.tar.gz"
mkdir -p "${tmp}/tree/${stage_name}"
printf 'fake-binary\n' > "${tmp}/tree/${stage_name}/WeKnora-lite"
printf 'Test License\n' > "${tmp}/tree/${stage_name}/LICENSE"
printf 'Third party notices\n' > "${tmp}/tree/${stage_name}/THIRD_PARTY_NOTICES.md"
tar -czf "${tmp}/${archive_name}" -C "${tmp}/tree" "$stage_name"

archive_sha=$(shasum -a 256 "${tmp}/${archive_name}" | awk '{print $1}')
binary_sha=$(shasum -a 256 "${tmp}/tree/${stage_name}/WeKnora-lite" | awk '{print $1}')
license_sha=$(shasum -a 256 "${tmp}/tree/${stage_name}/LICENSE" | awk '{print $1}')
notices_sha=$(shasum -a 256 "${tmp}/tree/${stage_name}/THIRD_PARTY_NOTICES.md" | awk '{print $1}')

manifest="${tmp}/good.manifest.json"
write_lite_manifest "$manifest" "$source_sha" "0.0.0" "darwin" "arm64" "go1.26.0" "2020-01-02T03:04:05Z" \
    "$archive_name" "$archive_sha" "$binary_sha" "Test License" "$license_sha" "$notices_sha" true
sign_lite_manifest "${tmp}/key" "$manifest"

pub_two=$(ssh-keygen -y -f "${tmp}/key" | cut -d' ' -f1,2)
printf 'agent24-lite %s\n' "$pub_two" > "${tmp}/allowed"

# 1. Matching signature, hashes, and expected SHA.
if bash "$verify" "$manifest" "${tmp}/${archive_name}" "${tmp}/allowed" "$source_sha" >"${tmp}/t1.out" 2>"${tmp}/t1.err"; then
    if grep -F "OK ${source_sha} darwin/arm64" "${tmp}/t1.out" >/dev/null; then
        pass "verify accepts a matching signed manifest"
    else
        fail_case "verify accepts a matching signed manifest"
        cat "${tmp}/t1.out" >&2
    fi
else
    fail_case "verify accepts a matching signed manifest"
    cat "${tmp}/t1.err" >&2
fi

# 2. One changed archive byte fails archive_sha256. Basename stays the same.
mkdir -p "${tmp}/tampered"
cp "${tmp}/${archive_name}" "${tmp}/tampered/${archive_name}"
first=$(dd if="${tmp}/tampered/${archive_name}" bs=1 count=1 2>/dev/null)
if [ "$first" = "A" ]; then
    repl="B"
else
    repl="A"
fi
printf '%s' "$repl" | dd of="${tmp}/tampered/${archive_name}" bs=1 count=1 conv=notrunc >/dev/null 2>&1
if bash "$verify" "$manifest" "${tmp}/tampered/${archive_name}" "${tmp}/allowed" "$source_sha" >"${tmp}/t2.out" 2>"${tmp}/t2.err"; then
    fail_case "tampered archive is rejected"
else
    if grep archive_sha256 "${tmp}/t2.err" >/dev/null; then
        pass "tampered archive is rejected"
    else
        fail_case "tampered archive is rejected"
        cat "${tmp}/t2.err" >&2
    fi
fi

# 3. Changing arch invalidates the signature copied from the good manifest.
sed 's/"arch":"arm64"/"arch":"amd64"/' "$manifest" > "${tmp}/arch.manifest.json"
cp "${manifest}.sig" "${tmp}/arch.manifest.json.sig"
if bash "$verify" "${tmp}/arch.manifest.json" "${tmp}/${archive_name}" "${tmp}/allowed" "$source_sha" >"${tmp}/t3.out" 2>"${tmp}/t3.err"; then
    fail_case "changed arch is rejected"
else
    pass "changed arch is rejected"
fi

# 4. A different expected SHA fails source_sha.
other_sha="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
if bash "$verify" "$manifest" "${tmp}/${archive_name}" "${tmp}/allowed" "$other_sha" >"${tmp}/t4.out" 2>"${tmp}/t4.err"; then
    fail_case "unexpected source SHA is rejected"
else
    if grep source_sha "${tmp}/t4.err" >/dev/null; then
        pass "unexpected source SHA is rejected"
    else
        fail_case "unexpected source SHA is rejected"
        cat "${tmp}/t4.err" >&2
    fi
fi

# 5. signed:false and no signature file fails.
write_lite_manifest "${tmp}/unsigned.manifest.json" "$source_sha" "0.0.0" "darwin" "arm64" "go1.26.0" "2020-01-02T03:04:05Z" \
    "$archive_name" "$archive_sha" "$binary_sha" "Test License" "$license_sha" "$notices_sha" false
if bash "$verify" "${tmp}/unsigned.manifest.json" "${tmp}/${archive_name}" "${tmp}/allowed" "$source_sha" >"${tmp}/t5.out" 2>"${tmp}/t5.err"; then
    fail_case "unsigned manifest without signature is rejected"
else
    pass "unsigned manifest without signature is rejected"
fi

# 6. Build-script argument checks exit before go build.
if bash "$build" abc123 --out "${tmp}/out-short" --unsigned >"${tmp}/t6.out" 2>"${tmp}/t6.err"; then
    fail_case "short SHA is rejected"
else
    pass "short SHA is rejected"
fi

zero_sha="0000000000000000000000000000000000000000"
if bash "$build" "$zero_sha" --out "${tmp}/out-zero" --unsigned >"${tmp}/t7.out" 2>"${tmp}/t7.err"; then
    fail_case "unknown SHA is rejected"
else
    if grep ERROR "${tmp}/t7.err" >/dev/null; then
        pass "unknown SHA is rejected"
    else
        fail_case "unknown SHA is rejected"
        cat "${tmp}/t7.err" >&2
    fi
fi

head_sha=$(git -C "$repo_root" rev-parse HEAD)
if bash "$build" "$head_sha" --out "${tmp}/out-nosign" >"${tmp}/t8.out" 2>"${tmp}/t8.err"; then
    fail_case "missing sign mode is rejected"
else
    pass "missing sign mode is rejected"
fi

if [ "$fail" -ne 0 ]; then
    exit 1
fi
