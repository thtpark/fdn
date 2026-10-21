#!/bin/sh
set -eu

bundle=
trusted=
prefix=
stopped=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --bundle) bundle=$2; shift 2 ;;
    --manifest-sha256) trusted=$2; shift 2 ;;
    --prefix) prefix=$2; shift 2 ;;
    --acknowledge-stopped) stopped=true; shift ;;
    *) printf '%s\n' 'Unknown option. Use --bundle DIR --manifest-sha256 TRUSTED_DIGEST --prefix DIR --acknowledge-stopped.' >&2; exit 2 ;;
  esac
done
[ -n "$bundle" ] && [ -n "$trusted" ] && [ -n "$prefix" ] && [ "$stopped" = true ] || {
  printf '%s\n' 'An explicit bundle, independently trusted digest, prefix, and stopped-process acknowledgement are required.' >&2
  exit 2
}
case "$trusted" in *[!0-9a-f]*|'') printf '%s\n' 'Invalid digest.' >&2; exit 2 ;; esac
[ "${#trusted}" -eq 64 ] || { printf '%s\n' 'Invalid digest length.' >&2; exit 2; }
case "$prefix" in /*) ;; *) printf '%s\n' 'Prefix must be absolute.' >&2; exit 2 ;; esac
bundle=$(cd "$bundle" && pwd -P)
stage=$(mktemp -d "${TMPDIR:-/tmp}/fdn-verify.XXXXXXXX")
install_stage=
trap 'if [ -n "$stage" ] && [ -d "$stage" ]; then rm -r "$stage"; fi; if [ -n "$install_stage" ] && [ -d "$install_stage" ]; then rm -r "$install_stage"; fi' EXIT HUP INT TERM
cp "$bundle/SHA256SUMS" "$stage/SHA256SUMS"
chmod 0600 "$stage/SHA256SUMS"
manifest="$stage/SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$manifest" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$manifest" | awk '{print $1}')
fi
[ "$actual" = "$trusted" ] || { printf '%s\n' 'Untrusted checksum manifest.' >&2; exit 4; }
awk 'NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/ || $2 == "SHA256SUMS" || $2 !~ /^[a-zA-Z0-9][a-zA-Z0-9._+-]*$/ {exit 1}' "$manifest" || {
  printf '%s\n' 'Unsafe checksum entry.' >&2; exit 4
}
while read -r digest name; do
  cp "$bundle/$name" "$stage/$name"
done < "$manifest"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$stage" && sha256sum -c SHA256SUMS)
else
  (cd "$stage" && shasum -a 256 -c SHA256SUMS)
fi
case "$(uname -s):$(uname -m)" in
  Linux:x86_64) target=linux-amd64 ;;
  Linux:aarch64|Linux:arm64) target=linux-arm64 ;;
  Darwin:arm64) target=darwin-arm64 ;;
  *) printf '%s\n' 'Unsupported native platform.' >&2; exit 6 ;;
esac
set -- "$stage"/fdn-*-"$target".tar.gz
[ "$#" -eq 1 ] && [ -f "$1" ] || { printf '%s\n' 'Expected one matching native archive.' >&2; exit 4; }
archive=$1
tar -tzf "$archive" > "$stage/archive-paths"
awk '
  seen[$0]++ || $0 ~ /(^|\/)\.\.?($|\/)/ || $0 ~ /\/\// {exit 1}
  $0 == "THIRD_PARTY_NOTICES" || $0 == "bin/agent-cli" || $0 == "bin/agent-service" || $0 == "bin/fdn-service" {next}
  $0 == "guide/MANIFEST.json" || $0 ~ /^guide\/([a-zA-Z0-9._-]+\/)*[a-zA-Z0-9._-]+\.md$/ {next}
  {exit 1}
' "$stage/archive-paths" || { printf '%s\n' 'Unexpected archive paths or duplicate entries.' >&2; exit 4; }
for required in THIRD_PARTY_NOTICES bin/agent-cli bin/agent-service bin/fdn-service guide/MANIFEST.json guide/AGENTS.md guide/README.md guide/DEVELOPMENT.md guide/DEPLOYMENT.md guide/COMMANDS.md guide/SCHEMA.md guide/TEST_PLAN.md guide/IMPLEMENTATION_PLAN.md guide/CAPACITY.md guide/MIGRATION.md; do
  grep -Fxq "$required" "$stage/archive-paths" || { printf '%s\n' 'Incomplete binaries or operating guide.' >&2; exit 4; }
done
for skill in foundation-install foundation-enroll foundation-provider-auth foundation-work foundation-operations foundation-develop; do
  grep -Fxq "guide/.agents/skills/$skill/SKILL.md" "$stage/archive-paths" || { printf '%s\n' 'Missing operational skill.' >&2; exit 4; }
done
tar -tvzf "$archive" | awk 'substr($0,1,1) != "-" {exit 1}' || { printf '%s\n' 'Only regular release entries are allowed.' >&2; exit 4; }
mkdir -p "$prefix/lib/fdn" "$prefix/bin"
release="$prefix/lib/fdn/$(basename "$archive" .tar.gz)"
if [ -e "$release" ]; then
  [ -f "$release/manifest.sha256" ] && [ "$(cat "$release/manifest.sha256")" = "$trusted" ] || {
    printf '%s\n' 'Immutable release already exists with different provenance; increment VERSION.' >&2; exit 5
  }
else
  mkdir "$stage/unpacked"
  tar -xzf "$archive" -C "$stage/unpacked"
  chmod 0755 "$stage/unpacked" "$stage/unpacked/bin" "$stage/unpacked/bin/agent-cli" "$stage/unpacked/bin/agent-service" "$stage/unpacked/bin/fdn-service"
  find "$stage/unpacked/guide" -type d -exec chmod 0755 {} +
  find "$stage/unpacked/guide" -type f -exec chmod 0644 {} +
  printf '%s\n' "$trusted" > "$stage/unpacked/manifest.sha256"
  install_stage=$(mktemp -d "$prefix/lib/fdn/.install.XXXXXXXX")
  cp -R "$stage/unpacked/." "$install_stage/"
  chmod 0755 "$install_stage"
  mv "$install_stage" "$release"
  install_stage=
fi
for binary in agent-cli agent-service fdn-service; do
  destination="$prefix/bin/$binary"
  if [ -e "$destination" ] || [ -L "$destination" ]; then
    [ -L "$destination" ] && [ "$(readlink "$destination")" = "$prefix/lib/fdn/current/bin/$binary" ] || {
      printf '%s\n' 'Refusing to replace an unmanaged binary or symlink.' >&2; exit 5
    }
  fi
done
for binary in agent-cli agent-service fdn-service; do
  destination="$prefix/bin/$binary"
  if [ ! -L "$destination" ]; then ln -s "$prefix/lib/fdn/current/bin/$binary" "$destination"; fi
done
current="$prefix/lib/fdn/current"
if [ -e "$current" ] && [ ! -L "$current" ]; then
  printf '%s\n' 'Refusing to replace an unmanaged activation path.' >&2; exit 5
fi
temporary="$prefix/lib/fdn/.current-$$"
ln -s "$release" "$temporary"
case "$target" in
  darwin-*) mv -fh "$temporary" "$current" ;;
  *) mv -Tf "$temporary" "$current" ;;
esac
printf '%s\n' 'Verified binaries installed. Configuration, identity, knowledge, workspaces, and outboxes were not changed. Start services explicitly after checking compatibility.'
