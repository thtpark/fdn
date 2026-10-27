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
for directory in "$HOME" "$HOME/.bin" "$HOME/.config" "$HOME/.config/fdn" "$HOME/.state" "$HOME/.state/identity" "$HOME/.state/ipc"; do
  if [ -L "$directory" ]; then
    printf '%s\n' 'An isolated testing directory is a symlink; normal HOME will not be adopted.' >&2
    exit 4
  fi
  mkdir -p "$directory"
  if [ -n "$(find "$directory" -maxdepth 0 \( ! -user "$(id -u)" -o -perm -0022 \) -print)" ]; then
    printf '%s\n' 'Testing directories must belong to this user without group/other writes.' >&2
    exit 4
  fi
done
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_DATA_HOME="$HOME/.data"
export XDG_CACHE_HOME="$HOME/.cache"
export CODEX_HOME="$HOME/.codex"
export GH_CONFIG_DIR="$HOME/.config/gh"
export CLOUDSDK_CONFIG="$HOME/.config/gcloud"
unset SSH_AUTH_SOCK GITHUB_TOKEN GH_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN
unset OPENAI_API_KEY CODEX_API_KEY COPILOT_GITHUB_TOKEN GOOGLE_APPLICATION_CREDENTIALS
unset CLOUDSDK_AUTH_ACCESS_TOKEN CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT
export PATH="$HOME/.bin:$HOME/.local/bin:$PATH"
identity_config="$HOME/.config/fdn/identity.json"
if [ -L "$identity_config" ]; then
  printf '%s\n' 'Testing identity configuration must not be a symlink.' >&2
  exit 4
fi
if [ ! -e "$identity_config" ]; then
  printf '{"state_dir":"%s","socket":"%s"}\n' "$HOME/.state/identity" "$HOME/.state/ipc/identity.sock" > "$identity_config"
fi
version=$(tr -d '\r\n' < "$root/VERSION")
commit=$(git -C "$root" rev-parse HEAD)
if [ -n "$(git -C "$root" status --porcelain --untracked-files=normal)" ]; then commit="$commit-dirty"; fi
build=$(mktemp -d "$HOME/.bin/.build.XXXXXXXX")
trap 'rm -f -- "$build/agent-cli" "$build/agent-service" "$build/agent-identity"; rmdir "$build"' EXIT
for target in agent-cli agent-service agent-identity; do
  if [ -L "$HOME/.bin/$target" ] || { [ -e "$HOME/.bin/$target" ] && [ ! -f "$HOME/.bin/$target" ]; }; then
    printf '%s\n' 'A development binary destination is a symlink or non-file; it will not be adopted.' >&2
    exit 4
  fi
  (
    cd "$root"
    go build -buildvcs=false \
      -ldflags "-X github.com/thtpark/fdn/internal/domain.Version=$version-dev -X github.com/thtpark/fdn/internal/domain.Commit=$commit" \
      -o "$build/$target" "./cmd/$target"
  )
  mv -f "$build/$target" "$HOME/.bin/$target"
done
rmdir "$build"
trap - EXIT
case "$binary" in
  agent-identity)
    case "${1:-}" in
      --help|-h|help|service) exec "$HOME/.bin/$binary" "$@" ;;
      *) exec "$HOME/.bin/$binary" --config "$identity_config" "$@" ;;
    esac
    ;;
  *) exec "$HOME/.bin/$binary" --config "$HOME/.config/fdn/agent.json" "$@" ;;
esac
