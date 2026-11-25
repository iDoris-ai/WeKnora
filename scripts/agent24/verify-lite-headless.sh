#!/bin/bash
# Agent24 fork product patch A24-KB-61 (KB-LITE-01b): verify a headless Lite manifest, signature, and archive hashes.
set -euo pipefail

# field prints the string value of "key":"value" from the single-line manifest.
field() {
    key=$1
    printf '%s\n' "$manifest_line" | sed -n "s/.*\"${key}\":\"\\([^\"]*\\)\".*/\\1/p"
}

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
    echo "ERROR: usage: verify-lite-headless.sh <manifest.json> <archive.tar.gz> <allowed_signers> [<expected SHA>]" >&2
    exit 1
fi

manifest=$1
archive=$2
allowed=$3

if [ ! -f "$manifest" ]; then
    echo "ERROR: manifest" >&2
    exit 1
fi
if [ ! -f "$archive" ]; then
    echo "ERROR: archive" >&2
    exit 1
fi
if [ ! -f "$allowed" ]; then
    echo "ERROR: allowed_signers" >&2
    exit 1
fi

sig="${manifest}.sig"
if [ ! -f "$sig" ]; then
    echo "ERROR: signature" >&2
    exit 1
fi
if ! ssh-keygen -Y verify -f "$allowed" -I agent24-lite -n agent24-lite-manifest -s "$sig" < "$manifest"; then
    echo "ERROR: signature" >&2
    exit 1
fi

manifest_line=$(head -n 1 "$manifest" | tr -d '\r')

case "$manifest_line" in
    *'"signed":true}'|*'"signed":true,'*)
        ;;
    *)
        echo "ERROR: signed" >&2
        exit 1
        ;;
esac

archive_base=$(basename "$archive")
manifest_archive=$(field archive)
if [ -z "$manifest_archive" ] || [ "$manifest_archive" != "$archive_base" ]; then
    echo "ERROR: archive" >&2
    exit 1
fi

if ! got_archive=$(shasum -a 256 "$archive" | awk '{print $1}'); then
    echo "ERROR: archive_sha256" >&2
    exit 1
fi
want_archive=$(field archive_sha256)
if [ -z "$want_archive" ] || [ "$got_archive" != "$want_archive" ]; then
    echo "ERROR: archive_sha256" >&2
    exit 1
fi

if [ "$#" -eq 4 ]; then
    got_sha=$(field source_sha)
    if [ "$got_sha" != "$4" ]; then
        echo "ERROR: source_sha" >&2
        exit 1
    fi
fi

extract=$(mktemp -d "${TMPDIR:-/tmp}/lite-verify.XXXXXX") || {
    echo "ERROR: mktemp failed" >&2
    exit 1
}
cleanup() {
    status=$?
    if [ -n "${extract}" ] && [ -d "${extract}" ]; then
        rm -rf "${extract}"
    fi
    exit "$status"
}
trap cleanup EXIT

if ! tar -xzf "$archive" -C "$extract"; then
    echo "ERROR: tar" >&2
    exit 1
fi

stage=${archive_base%.tar.gz}
binary="${extract}/${stage}/WeKnora-lite"
license_file="${extract}/${stage}/LICENSE"
if [ ! -f "$binary" ]; then
    echo "ERROR: binary" >&2
    exit 1
fi
if [ ! -f "$license_file" ]; then
    echo "ERROR: license" >&2
    exit 1
fi

if ! got_binary=$(shasum -a 256 "$binary" | awk '{print $1}'); then
    echo "ERROR: binary_sha256" >&2
    exit 1
fi
want_binary=$(field binary_sha256)
if [ -z "$want_binary" ] || [ "$got_binary" != "$want_binary" ]; then
    echo "ERROR: binary_sha256" >&2
    exit 1
fi

if ! got_license=$(shasum -a 256 "$license_file" | awk '{print $1}'); then
    echo "ERROR: license_sha256" >&2
    exit 1
fi
want_license=$(field license_sha256)
if [ -z "$want_license" ] || [ "$got_license" != "$want_license" ]; then
    echo "ERROR: license_sha256" >&2
    exit 1
fi

source_sha=$(field source_sha)
os_name=$(field os)
arch_name=$(field arch)
if [ -z "$source_sha" ] || [ -z "$os_name" ] || [ -z "$arch_name" ]; then
    echo "ERROR: manifest fields" >&2
    exit 1
fi
printf 'OK %s %s/%s\n' "$source_sha" "$os_name" "$arch_name"
