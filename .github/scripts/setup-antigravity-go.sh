#!/usr/bin/env bash
set -euo pipefail

# The native TLS profile is part of this pinned toolchain.
revision=298f53e483ef15096a05b1d3017cb9d4207cb327
bootstrap="$(go env GOROOT)"
host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
root="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/cpa-go-${revision}"
if [[ ! -x "$root/bin/go" && ! -x "$root/bin/go.exe" ]]; then
  git init "$root"
  git -C "$root" fetch --depth=1 https://github.com/golang/go.git "$revision"
  git -C "$root" checkout --detach FETCH_HEAD
  (
    cd "$root/src"
    export GOROOT_BOOTSTRAP="$bootstrap" GOOS="$host_os" GOARCH="$host_arch" GOTOOLCHAIN=local
    if [[ "$host_os" == windows ]]; then
      MSYS2_ARG_CONV_EXCL='*' cmd.exe /c make.bat
    else
      ./make.bash
    fi
  )
fi
native_root="$root"
if [[ "$host_os" == windows ]]; then native_root="$(cygpath -w "$root")"; fi
export GOROOT="$native_root" GOTOOLCHAIN=local
export PATH="$root/bin:$PATH"
go version | grep -q 'devel_298f53e483'
if [[ -n "${GITHUB_ENV:-}" ]]; then
  printf 'GOROOT=%s\nGOTOOLCHAIN=local\n' "$native_root" >> "$GITHUB_ENV"
  printf '%s/bin\n' "$native_root" >> "$GITHUB_PATH"
fi
