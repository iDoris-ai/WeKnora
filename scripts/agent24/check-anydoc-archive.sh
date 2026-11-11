#!/usr/bin/env bash
# Agent24 fork build fix: check that a Linux anydoc static archive carries none of the sections go1.26.11+ rejects in
# a #cgo LDFLAGS -L directory (src/cmd/go/internal/work/security.go: isDisallowedLTOSection and the ELF section-type
# checks), so the go build that links it is not refused with "disallowed LTO section".
#
#   scripts/agent24/check-anydoc-archive.sh ARCHIVE
#
# Every member is read with readelf. Rejected section names: .gnu.lto_*, .gnu.debuglto_*, .llvm.lto*, .llvmbc,
# .llvmcmd. Rejected section types: 0x6fff4c01 (SHT_LLVM_LINKER_OPTIONS), 0x6fff4c04 (SHT_LLVM_DEPENDENT_LIBRARIES)
# and 0x6fff4c0c (SHT_LLVM_LTO), which readelf prints either as the hex value or as the LLVM_* name. Exit 0 only
# when the archive exists, has members and none of them carries one; anything readelf cannot read is a failure.
set -euo pipefail

archive=${1:?usage: check-anydoc-archive.sh ARCHIVE}
readelf=${READELF:-readelf}

if [ ! -s "$archive" ]; then
  echo "error: $archive is missing or empty" >&2
  exit 1
fi

out=$("$readelf" -S -W "$archive" 2>&1) || {
  echo "error: $readelf could not read $archive" >&2
  exit 1
}
members=$(printf '%s\n' "$out" | grep -c '^File: ' || true)
if [ "$members" -eq 0 ]; then
  echo "error: $archive has no ELF members readelf could list" >&2
  exit 1
fi
if printf '%s\n' "$out" | grep -q -E "readelf: (Error|Warning)"; then
  echo "error: readelf reported problems reading $archive" >&2
  exit 1
fi

bad=$(printf '%s\n' "$out" | grep -E \
  '\] (\.gnu\.lto_|\.gnu\.debuglto_|\.llvm\.lto|\.llvmbc |\.llvmcmd )|0x6fff4c0[14c]|LLVM_LTO|LLVM_LINKER_OPTIONS|LLVM_DEPENDENT_LIBRARIES' \
  || true)
if [ -n "$bad" ]; then
  echo "error: $archive still carries sections go1.26.11+ rejects:" >&2
  printf '%s\n' "$bad" | sort -u | head -20 >&2
  exit 1
fi

echo "ok: $archive ($members members) carries no LTO/bitcode sections"
