#!/bin/bash
# Agent24 fork product patch A24-KB-61 (KB-LITE-01b): write and sign a headless Lite manifest.
# Sourced by build-lite-headless.sh and test-lite-headless.sh. Not an entry point.

# json_escape prints $1 safe to place inside a JSON string.
json_escape() {
    printf '%s' "$1" | awk '
        BEGIN { ORS = "" }
        {
            gsub(/\\/, "\\\\")
            gsub(/"/, "\\\"")
            print
        }
    '
}

# write_lite_manifest DEST SOURCE_SHA VERSION OS ARCH GO_VERSION COMMIT_TIME
#   ARCHIVE ARCHIVE_SHA256 BINARY_SHA256 LICENSE LICENSE_SHA256 NOTICES_SHA256 SIGNED
# SIGNED is the JSON boolean true or false. One JSON object, one trailing newline.
write_lite_manifest() {
    local dest source_sha version os_name arch_name go_version commit_time
    local archive_name archive_sha256 binary_sha256 license_text license_sha256
    local notices_sha256 signed_value
    local version_esc os_esc arch_esc go_esc time_esc archive_esc license_esc
    local sha_esc archive_hex_esc binary_hex_esc license_hex_esc notices_hex_esc

    if [ "$#" -ne 14 ]; then
        echo "ERROR: write_lite_manifest expected 14 arguments" >&2
        return 1
    fi
    dest=$1
    source_sha=$2
    version=$3
    os_name=$4
    arch_name=$5
    go_version=$6
    commit_time=$7
    archive_name=$8
    archive_sha256=$9
    binary_sha256=${10}
    license_text=${11}
    license_sha256=${12}
    notices_sha256=${13}
    signed_value=${14}

    if [ "$signed_value" != "true" ] && [ "$signed_value" != "false" ]; then
        echo "ERROR: signed must be true or false" >&2
        return 1
    fi

    version_esc=$(json_escape "$version")
    os_esc=$(json_escape "$os_name")
    arch_esc=$(json_escape "$arch_name")
    go_esc=$(json_escape "$go_version")
    time_esc=$(json_escape "$commit_time")
    archive_esc=$(json_escape "$archive_name")
    license_esc=$(json_escape "$license_text")
    sha_esc=$(json_escape "$source_sha")
    archive_hex_esc=$(json_escape "$archive_sha256")
    binary_hex_esc=$(json_escape "$binary_sha256")
    license_hex_esc=$(json_escape "$license_sha256")
    notices_hex_esc=$(json_escape "$notices_sha256")

    printf '%s\n' "{\"schema\":1,\"name\":\"WeKnora-lite-headless\",\"source_sha\":\"${sha_esc}\",\"version\":\"${version_esc}\",\"edition\":\"lite\",\"os\":\"${os_esc}\",\"arch\":\"${arch_esc}\",\"go_version\":\"${go_esc}\",\"build_tags\":\"sqlite_fts5\",\"commit_time\":\"${time_esc}\",\"archive\":\"${archive_esc}\",\"archive_sha256\":\"${archive_hex_esc}\",\"binary_sha256\":\"${binary_hex_esc}\",\"license\":\"${license_esc}\",\"license_sha256\":\"${license_hex_esc}\",\"third_party_notices_sha256\":\"${notices_hex_esc}\",\"signed\":${signed_value}}" > "$dest"
}

# sign_lite_manifest KEY MANIFEST writes MANIFEST.sig.
sign_lite_manifest() {
    local key manifest
    if [ "$#" -ne 2 ]; then
        echo "ERROR: sign_lite_manifest expected 2 arguments" >&2
        return 1
    fi
    key=$1
    manifest=$2
    # ssh-keygen writes "Signing file..." to stdout. Keep the build script's
    # stdout limited to the ARCHIVE/MANIFEST/SIG lines.
    if ! ssh-keygen -Y sign -f "$key" -n agent24-lite-manifest "$manifest" >&2; then
        echo "ERROR: ssh-keygen sign failed" >&2
        return 1
    fi
}
