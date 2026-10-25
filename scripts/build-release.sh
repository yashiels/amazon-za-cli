#!/bin/bash
set -euo pipefail
umask 022

work=

cleanup() {
  status=$?
  if [ -n "${work:-}" ] && [ -d "$work" ]; then
    case "$work" in
      */amazon-release.????????) ;;
      *)
        echo "refusing to clean unexpected work directory" >&2
        exit 1
        ;;
    esac
    case "$work" in
      "${out:-}"|"${out:-}"/*)
        echo "refusing to clean output directory" >&2
        exit 1
        ;;
    esac
    rm -f "$work"/amazon_*.tar.gz "$work"/checksums.txt "$work"/darwin/*/amazon "$work"/linux/*/amazon
    rmdir "$work"/darwin/arm64 "$work"/darwin/amd64 "$work"/linux/arm64 "$work"/linux/amd64 "$work"/darwin "$work"/linux "$work" 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT

if [ "$#" -ne 2 ]; then
  echo "usage: scripts/build-release.sh VERSION OUTPUT_DIR" >&2
  exit 2
fi

version=$1
raw_out=$2

if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  echo "version must be strict X.Y.Z" >&2
  exit 1
fi

prepare_output() {
  raw=$1
  if [ -z "$raw" ] || [ "$raw" = "/" ] || [ "$raw" = "." ] || [ "$raw" = ".." ]; then
    echo "unsafe output directory" >&2
    exit 1
  fi
  case "$raw" in
    -*|*[!A-Za-z0-9._+/-]*|*//*)
      echo "unsafe output directory" >&2
      exit 1
      ;;
  esac
  trimmed=$raw
  while :; do
    case "$trimmed" in
      ./*) trimmed=${trimmed#./} ;;
      *) break ;;
    esac
  done
  if [ -z "$trimmed" ]; then
    echo "unsafe output directory" >&2
    exit 1
  fi
  rest=$trimmed
  while [ -n "$rest" ]; do
    case "$rest" in
      /*) rest=${rest#/} ;;
    esac
    if [ -z "$rest" ]; then
      break
    fi
    case "$rest" in
      */*) comp=${rest%%/*}; rest=${rest#*/} ;;
      *) comp=$rest; rest= ;;
    esac
    case "$comp" in
      ""|.|..)
        echo "unsafe output directory" >&2
        exit 1
        ;;
    esac
  done
  case "$trimmed" in
    /*) absolute=$trimmed ;;
    *) absolute=$PWD/$trimmed ;;
  esac
  if [ -L "$absolute" ]; then
    echo "refusing symlink output directory" >&2
    exit 1
  fi
  if [ -e "$absolute" ]; then
    if [ ! -d "$absolute" ] || [ ! -O "$absolute" ] || [ ! -w "$absolute" ]; then
      echo "unsafe output directory" >&2
      exit 1
    fi
  else
    parent=$(dirname "$absolute")
    if [ -L "$parent" ] || [ ! -d "$parent" ] || [ ! -O "$parent" ] || [ ! -w "$parent" ]; then
      echo "unsafe output directory" >&2
      exit 1
    fi
    mkdir "$absolute"
  fi
  absolute=$(cd "$absolute" && pwd -P)
  if [ "$absolute" = "/" ]; then
    echo "unsafe output directory" >&2
    exit 1
  fi
  prepared=$absolute
}

prepare_output "$raw_out"
out=$prepared
root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

host_os=$(uname -s)
case "$host_os" in
  Darwin) host_os=darwin ;;
  Linux) host_os=linux ;;
  *)
    echo "unsupported host OS" >&2
    exit 1
    ;;
esac
host_arch=$(uname -m)
case "$host_arch" in
  arm64|aarch64) host_arch=arm64 ;;
  x86_64|amd64) host_arch=amd64 ;;
  *)
    echo "unsupported host architecture" >&2
    exit 1
    ;;
esac

command -v go >/dev/null 2>&1
command -v tar >/dev/null 2>&1
command -v gzip >/dev/null 2>&1
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/amazon-release.XXXXXXXX")
case "$work" in
  "$out"|"$out"/*)
    echo "refusing work directory inside output" >&2
    exit 1
    ;;
esac

smoke() {
  bin=$1
  got=$("$bin" --version)
  if [ "$got" != "$version" ]; then
    echo "host version smoke returned '${got}'" >&2
    exit 1
  fi
  "$bin" --help >/dev/null
  set +e
  info_out=$(
    HTTP_PROXY=http://127.0.0.1:9 \
    HTTPS_PROXY=http://127.0.0.1:9 \
    http_proxy=http://127.0.0.1:9 \
    https_proxy=http://127.0.0.1:9 \
    ALL_PROXY=http://127.0.0.1:9 \
    all_proxy=http://127.0.0.1:9 \
    NO_PROXY= \
    no_proxy= \
    "$bin" info "https://example.com/dp/B0H471LF7H" --json
  )
  info_status=$?
  set -e
  if [ "$info_status" -ne 4 ]; then
    echo "foreign product URL smoke exited ${info_status}" >&2
    exit 1
  fi
  case "$info_out" in
    *'"code":"usage"'*) ;;
    *)
      echo "foreign product URL smoke was not a usage error" >&2
      exit 1
      ;;
  esac
}

smoked=0
for spec in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os=${spec%/*}
  arch=${spec#*/}
  mkdir -p "$work/$os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "$work/$os/$arch/amazon" ./cmd/amazon
  if [ "$os" = "$host_os" ] && [ "$arch" = "$host_arch" ]; then
    smoke "$work/$os/$arch/amazon"
    smoked=1
  fi
done
if [ "$smoked" -ne 1 ]; then
  echo "host binary was not smoked" >&2
  exit 1
fi

archives=(
  "amazon_${version}_darwin_amd64.tar.gz"
  "amazon_${version}_darwin_arm64.tar.gz"
  "amazon_${version}_linux_amd64.tar.gz"
  "amazon_${version}_linux_arm64.tar.gz"
)
specs=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64)
index=0
while [ "$index" -lt 4 ]; do
  spec=${specs[$index]}
  os=${spec%/*}
  arch=${spec#*/}
  name=${archives[$index]}
  (
    cd "$work/$os/$arch"
    chmod 0755 amazon
    TZ=UTC touch -t 200001010000.00 amazon
    COPYFILE_DISABLE=1 tar -cf - --format ustar --owner=0 --group=0 amazon
  ) | gzip -n > "$work/$name"
  index=$((index + 1))
done

(
  cd "$work"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${archives[@]}"
  else
    shasum -a 256 "${archives[@]}"
  fi
) | awk '{printf "%s  %s\n", $1, $2}' > "$work/checksums.txt"

for name in "${archives[@]}" checksums.txt; do
  test -s "$work/$name"
  mv "$work/$name" "$out/$name"
done
