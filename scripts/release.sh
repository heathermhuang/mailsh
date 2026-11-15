#!/usr/bin/env bash
# Build release archives for one version: scripts/release.sh v0.1.0 → dist/*.tar.gz and dist/checksums.txt
set -euo pipefail
v="${1:?usage: scripts/release.sh vX.Y.Z}"
rm -rf dist && mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  dir="dist/mailsh_${v#v}_${os}_${arch}"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${v#v}" -o "$dir/mail.sh" .
  cp README.md LICENSE "$dir/"
  tar -C dist -czf "$dir.tar.gz" "$(basename "$dir")"
  rm -rf "$dir"
done
(cd dist && shasum -a 256 *.tar.gz > checksums.txt)
cat dist/checksums.txt
