#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

go_binary=${GO_BINARY:-go}
target_os=${GOOS:-$($go_binary env GOOS)}
target_arch=${GOARCH:-$($go_binary env GOARCH)}
output_dir=${1:-"$repo_root/dist"}
version=${VERSION:-${2:-}}

case "$version" in
  v*) version=${version#v} ;;
esac
if [ -n "$version" ] && ! printf '%s' "$version" | grep -Eq '^[0-9][0-9A-Za-z.+-]*$'; then
  printf '版本号格式无效: %s\n' "$version" >&2
  exit 1
fi

case "$output_dir" in
  /*) ;;
  *) output_dir="$repo_root/$output_dir" ;;
esac

case "$target_os" in
  windows) extension=.dll ;;
  darwin) extension=.dylib ;;
  *) extension=.so ;;
esac

mkdir -p "$output_dir"
artifact_name=cpa-auto-refresh-quota
ldflags='-s -w'
if [ -n "$version" ]; then
  artifact_name="$artifact_name-v$version"
  ldflags="$ldflags -X main.pluginVersion=$version"
fi
artifact="$output_dir/$artifact_name$extension"
header="$output_dir/$artifact_name.h"

(
  cd "$repo_root"
  CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
    "$go_binary" build \
      -buildvcs=false \
      -trimpath \
      -ldflags="$ldflags" \
      -buildmode=c-shared \
      -o "$artifact" \
      ./cmd/cpa-auto-refresh-quota
)

rm -f "$header"
printf 'Built %s for %s/%s\n' "$artifact" "$target_os" "$target_arch"
