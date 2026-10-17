#!/usr/bin/env bash
# Builds every target and lays the files out the way a GitHub Release (and
# scripts/install.sh) expects them, in dist/:
#   dyrokol-linux-amd64  dyrokol-linux-arm64  dyrokol-linux-arm
#   dyrokol-termux-arm64 dyrokol-termux-arm
#   dyrokol-windows-amd64.exe  dyrokol-windows-arm64.exe
#   SHA256SUMS
# Usage: scripts/release.sh   (then upload dist/* to the release page)
set -euo pipefail
cd "$(dirname "$0")/.."

scripts/build.sh
rm -rf dist && mkdir dist
for d in bin/*/; do
  t=$(basename "$d")
  case $t in
    windows-*) cp "$d/dyrokol.exe" "dist/dyrokol-$t.exe" ;;
    *)         cp "$d/dyrokol"     "dist/dyrokol-$t" ;;
  esac
done
(cd dist && sha256sum dyrokol-* >SHA256SUMS && cat SHA256SUMS)
