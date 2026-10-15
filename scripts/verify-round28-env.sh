#!/usr/bin/env bash
# Source before any subprocess: no OS-default scratch/cache writes.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)" || return 2
ROUND28_ROOT="$REPO_ROOT/.verification/round28"
export TMPDIR="$ROUND28_ROOT/tmp"
export TMP="$TMPDIR" TEMP="$TMPDIR"
export GOCACHE="$ROUND28_ROOT/cache/go-build"
export GOMODCACHE="$ROUND28_ROOT/cache/gomod"
export GOPROXY=off GOSUMDB=off
export PYTHONPYCACHEPREFIX="$ROUND28_ROOT/cache/python"
export npm_config_cache="$ROUND28_ROOT/cache/npm"
export XDG_CACHE_HOME="$ROUND28_ROOT/cache/xdg"
export HOME="$ROUND28_ROOT/home"
export XDG_CONFIG_HOME="$HOME/config"
export XDG_DATA_HOME="$HOME/data"
export XDG_RUNTIME_DIR="$ROUND28_ROOT/runtime"
export CGO_ENABLED=0
unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY
export NO_PROXY=127.0.0.1,localhost no_proxy=127.0.0.1,localhost
for verify_path in "$TMPDIR" "$GOCACHE" "$GOMODCACHE" "$PYTHONPYCACHEPREFIX" "$npm_config_cache" "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_RUNTIME_DIR" "$HOME"; do
  node "$REPO_ROOT/scripts/verify-round28-safety.mjs" --path "$verify_path" >/dev/null || return 2
done
for verify_path in "$TMPDIR" "$GOCACHE" "$GOMODCACHE" "$PYTHONPYCACHEPREFIX" "$npm_config_cache" "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_RUNTIME_DIR" "$HOME"; do
  mkdir -p "$verify_path" || return 2
  chmod 700 "$verify_path" || return 2
done
unset verify_path
