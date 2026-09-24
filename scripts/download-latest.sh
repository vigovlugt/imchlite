#!/bin/sh
set -eu

repo=vigovlugt/imchlite

url=$(curl -fsSL "https://api.github.com/repos/$repo/releases" \
  | sed -n 's/.*"browser_download_url": *"\([^"]*linux-amd64\.tar\.gz\)".*/\1/p' \
  | head -n 1)

[ -n "$url" ] || { echo "no release asset found" >&2; exit 1; }

curl -fL -o "$(basename "$url")" "$url"
