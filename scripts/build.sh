#!/usr/bin/env bash
# Cross-compiles dyrokol for every supported target into bin/<target>/.
# Usage: scripts/build.sh [target...]   (default: all targets)
set -euo pipefail

cd "$(dirname "$0")/.."

# target  GOOS  GOARCH  GOARM
TARGETS=(
  "linux-amd64    linux   amd64 -"
  "linux-arm64    linux   arm64 -"
  "linux-arm      linux   arm   7"
  "termux-arm64   linux   arm64 -"
  "termux-arm     linux   arm   7"
  "windows-amd64  windows amd64 -"
  "windows-arm64  windows arm64 -"
)

want=("$@")
selected() {
  [[ ${#want[@]} -eq 0 ]] && return 0
  local t
  for t in "${want[@]}"; do [[ $t == "$1" ]] && return 0; done
  return 1
}

for spec in "${TARGETS[@]}"; do
  read -r name goos goarch goarm <<<"$spec"
  selected "$name" || continue
  out="bin/$name/dyrokol"
  [[ $goos == windows ]] && out+=".exe"
  mkdir -p "bin/$name"
  vars=(CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch")
  [[ $goarm != - ]] && vars+=(GOARM="$goarm")
  env "${vars[@]}" go build -trimpath -buildvcs=false -ldflags "-s -w" -o "$out" ./cmd/dyrokol
  printf '%-15s %s (%s)\n' "$name" "$out" "$(du -h "$out" | cut -f1)"
done
