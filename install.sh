#!/bin/sh
# Install mail.sh: curl -fsSL https://raw.githubusercontent.com/heathermhuang/mailsh/main/install.sh | sh
set -eu
repo="heathermhuang/mailsh"
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;; esac
case "$os" in darwin|linux) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac
tag="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$tag" ] || { echo "could not find the latest release" >&2; exit 1; }
name="mailsh_${tag#v}_${os}_${arch}"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
base="https://github.com/$repo/releases/download/$tag"
curl -fsSL "$base/$name.tar.gz" -o "$tmp/$name.tar.gz"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
(cd "$tmp" && grep " $name.tar.gz\$" checksums.txt | shasum -a 256 -c - >/dev/null) || { echo "checksum mismatch" >&2; exit 1; }
tar -C "$tmp" -xzf "$tmp/$name.tar.gz"
dest="${MAILSH_INSTALL_DIR:-/usr/local/bin}"
if [ -w "$dest" ]; then mv "$tmp/$name/mail.sh" "$dest/mail.sh"; else sudo mv "$tmp/$name/mail.sh" "$dest/mail.sh"; fi
echo "Installed mail.sh $tag to $dest/mail.sh. Next: mail.sh auth"
