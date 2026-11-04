#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo 'Usage: tools/go-cross-build.sh <source-relative-dir> <output-relative-path> [go-package]' >&2
}

if (($# < 2 || $# > 3)); then
  usage
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_rel="$1"
output_rel="$2"
package="${3:-.}"
source_dir="$repo_root/$source_rel"
[[ -d "$source_dir" ]] || { echo "source directory missing: $source_dir" >&2; exit 2; }

goroot_win="$(cd "$source_dir" && go env GOROOT | tr -d '\r')"
go_exe_win="${goroot_win}\\bin\\go.exe"

/mnt/d/codexpro_workspace/ps-file "$repo_root/tools/go-cross-build.ps1" \
  -SourceRel "$source_rel" \
  -OutputRel "$output_rel" \
  -Package "$package" \
  -GoOs linux \
  -GoArch arm64 \
  -GoExePath "$go_exe_win"

output="$repo_root/$output_rel"
[[ -f "$output" ]] || { echo "output missing: $output" >&2; exit 3; }
kind="$(file -b "$output")"
echo "$kind"
if [[ "$kind" != *"ELF 64-bit"* || "$kind" != *"ARM aarch64"* ]]; then
  echo "unexpected binary format: $kind" >&2
  exit 4
fi
sha256sum "$output"
ls -lh "$output"
