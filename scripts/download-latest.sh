#!/bin/sh
set -eu

repo=vigovlugt/imchlite

arch=$(uname -m)
[ "$arch" = "x86_64" ] || { echo "unsupported architecture: $arch (only amd64)" >&2; exit 1; }

url=$(curl -fsSL "https://api.github.com/repos/$repo/releases" \
  | sed -n 's/.*"browser_download_url": *"\([^"]*linux-amd64\.tar\.gz\)".*/\1/p' \
  | head -n 1)

[ -n "$url" ] || { echo "no release asset found" >&2; exit 1; }

archive=$(basename "$url")
curl -fL -o "$archive" "$url"
tar -xzf "$archive"
rm -f "$archive"

echo "Downloaded imchlite"
