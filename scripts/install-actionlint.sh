#!/bin/bash
set -euo pipefail

version=1.7.12
case "$(uname -s)" in
  Linux) platform=linux ;;
  Darwin) platform=darwin ;;
  *)
    echo "unsupported OS" >&2
    exit 1
    ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *)
    echo "unsupported architecture" >&2
    exit 1
    ;;
esac

name="actionlint_${version}_${platform}_${arch}.tar.gz"
url="https://github.com/rhysd/actionlint/releases/download/v${version}/${name}"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/actionlint.XXXXXXXX")
cleanup() {
  status=$?
  if [ -n "${tmp:-}" ] && [ -d "$tmp" ]; then
    rm -f "$tmp/actionlint" "$tmp/actionlint.tar.gz"
    rmdir "$tmp" 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT

curl -fsSL -o "$tmp/actionlint.tar.gz" "$url"
tar -xzf "$tmp/actionlint.tar.gz" -C "$tmp" actionlint
dest=${PREFIX:-/usr/local/bin}
mkdir -p "$dest"
if [ -w "$dest" ]; then
  install -m 0755 "$tmp/actionlint" "$dest/actionlint"
else
  sudo install -m 0755 "$tmp/actionlint" "$dest/actionlint"
fi
"$dest/actionlint" -version
