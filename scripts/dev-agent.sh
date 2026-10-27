#!/bin/sh
set -eu
umask 077

case "${1:-}" in
  agent-cli|agent-service|agent-identity) binary=$1; shift ;;
  *) printf '%s\n' 'Use agent-cli.sh, agent-service.sh or agent-identity.sh.' >&2; exit 2 ;;
esac
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
command -v go >/dev/null 2>&1 || { printf '%s\n' 'Go is required for cached development builds.' >&2; exit 6; }
: "${HOME:?HOME is required}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOPATH="${GOPATH:-$(go env GOPATH)}"
export HOME="$root/.local"
case "$root" in *\"*|*\\*) printf '%s\n' 'Development checkout paths containing quotes or backslashes are unsupported.' >&2; exit 2 ;; esac
if printf '%s' "$root" | LC_ALL=C grep '[[:cntrl:]]' >/dev/null; then
  printf '%s\n' 'Development checkout paths must not contain control characters.' >&2
  exit 2
fi
foundation="$HOME/.foundation"
for directory in "$HOME" "$foundation" "$foundation/bin" "$foundation/config" "$foundation/state" "$foundation/identity" "$foundation/run"; do
  if [ -L "$directory" ]; then
    printf '%s\n' 'An isolated testing directory is a symlink; normal HOME will not be adopted.' >&2
    exit 4
  fi
  mkdir -p "$directory"
  if [ -n "$(find "$directory" -maxdepth 0 \( ! -user "$(id -u)" -o -perm -0020 -o -perm -0002 \) -print)" ]; then
    printf '%s\n' 'Testing directories must belong to this user without group/other writes.' >&2
    exit 4
  fi
  if [ "$directory" = "$foundation" ] && [ -n "$(find "$directory" -maxdepth 0 ! -perm 0700 -print)" ]; then
    printf '%s\n' 'The Foundation directory must be private (0700); existing permissions will not be changed automatically.' >&2
    exit 4
  fi
done
export PATH="$foundation/bin:$PATH"
identity_config="$foundation/config/identity.json"
if [ -L "$identity_config" ]; then
  printf '%s\n' 'Testing identity configuration must not be a symlink.' >&2
  exit 4
fi
if [ ! -e "$identity_config" ]; then
  printf '{"state_dir":"%s","socket":"%s"}\n' "$foundation/identity" "$foundation/run/identity.sock" > "$identity_config"
fi
version=$(tr -d '\r\n' < "$root/VERSION")
commit=$(git -C "$root" rev-parse HEAD)
if [ -n "$(git -C "$root" status --porcelain --untracked-files=normal)" ]; then commit="$commit-dirty"; fi
build=$(mktemp -d "$foundation/bin/.build.XXXXXXXX")
trap 'rm -f -- "$build/agent-cli" "$build/agent-service" "$build/agent-identity"; rmdir "$build"' EXIT
for target in agent-cli agent-service agent-identity; do
  if [ -L "$foundation/bin/$target" ] || { [ -e "$foundation/bin/$target" ] && [ ! -f "$foundation/bin/$target" ]; }; then
    printf '%s\n' 'A development binary destination is a symlink or non-file; it will not be adopted.' >&2
    exit 4
  fi
  (
    cd "$root"
    go build -buildvcs=false \
      -ldflags "-X github.com/thtpark/fdn/internal/domain.Version=$version-dev -X github.com/thtpark/fdn/internal/domain.Commit=$commit" \
      -o "$build/$target" "./cmd/$target"
  )
  mv -f "$build/$target" "$foundation/bin/$target"
done
rmdir "$build"
trap - EXIT
case "$binary" in
  agent-identity)
    case "${1:-}" in
      --help|-h|help|service) exec "$foundation/bin/$binary" "$@" ;;
      *) exec "$foundation/bin/$binary" --config "$identity_config" "$@" ;;
    esac
    ;;
  *) exec "$foundation/bin/$binary" --config "$foundation/config/agent.json" "$@" ;;
esac
