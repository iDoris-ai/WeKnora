#!/usr/bin/env bash
# Agent24 fork build fix: check that a Linux anydoc static archive carries none of the sections go1.26.11+ rejects in
# a #cgo LDFLAGS -L directory (src/cmd/go/internal/work/security.go: isDisallowedLTOSection and the ELF section-type
# checks), so the go build that links it is not refused with "disallowed LTO section".
#
#   scripts/agent24/check-anydoc-archive.sh ARCHIVE
#
# Every member is read with readelf. Rejected section names: .gnu.lto_*, .gnu.debuglto_*, .llvm.lto*, .llvmbc,
# .llvmcmd. Rejected section types: 0x6fff4c01 (SHT_LLVM_LINKER_OPTIONS), 0x6fff4c04 (SHT_LLVM_DEPENDENT_LIBRARIES)
# and 0x6fff4c0c (SHT_LLVM_LTO). Depending on the binutils version, readelf prints a type in the OS range as its
# LLVM_* name, as LOOS+0xfff4c0N, or as the bare hex value; all three spellings are matched, and only on section
# header lines. Exit 0 only when the archive exists, has members and none of them carries one; anything readelf
# cannot read is a failure.
set -euo pipefail

archive=${1:?usage: check-anydoc-archive.sh ARCHIVE}
readelf=${READELF:-readelf}
# readelf's "File:" header and its warnings go through gettext; the checks below match the untranslated text.
export LC_ALL=C

if [ ! -s "$archive" ]; then
  echo "error: $archive is missing or empty" >&2
  exit 1
fi

out=$("$readelf" -S -W "$archive" 2>&1) || {
  echo "error: $readelf could not read $archive" >&2
  exit 1
}
members=$(grep -c '^File: ' <<<"$out" || true)
if [ "$members" -eq 0 ]; then
  echo "error: $archive has no ELF members readelf could list" >&2
  exit 1
fi
# grep reads a here-string, not a pipe: an early match must not SIGPIPE the writer and, under pipefail, turn
# the condition false.
if grep -q -E "readelf: (Error|Warning)" <<<"$out"; then
  echo "error: readelf reported problems reading $archive" >&2
  exit 1
fi

# Section header lines only ("  [Nr] Name Type …"), so a member or path name cannot match.
sections=$(grep -E '^ *\[ *[0-9]+\] ' <<<"$out" || true)
bad=$(grep -E \
  '\] (\.gnu\.lto_|\.gnu\.debuglto_|\.llvm\.lto|\.llvmbc |\.llvmcmd )|\] [^ ]+ +(0x)?6fff4c0[14c] |\] [^ ]+ +LOOS\+0xfff4c0[14c] |\] [^ ]+ +LLVM_(LTO|LINKER_OPTIONS|DEPENDENT_LIBRARIES) ' \
  <<<"$sections" || true)
if [ -n "$bad" ]; then
  echo "error: $archive still carries sections go1.26.11+ rejects:" >&2
  printf '%s\n' "$bad" | sort -u | head -20 >&2
  exit 1
fi

echo "ok: $archive ($members members) carries no LTO/bitcode sections"
