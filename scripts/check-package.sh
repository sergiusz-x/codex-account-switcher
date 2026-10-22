#!/bin/sh
set -eu

version=${1:?Provide the expected embedded version.}
archive=codex_switch_linux_amd64.tar.gz
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT
trap 'exit 1' HUP INT TERM
cp "dist/$archive" dist/checksums.txt release-assets/install.sh "$work_dir/"
(cd "$work_dir" && sha256sum --check checksums.txt)
tar -xzf "dist/$archive" -C "$work_dir"
test -f "$work_dir/LICENSE"
find "$work_dir/release-assets/third_party_licenses" -type f -print -quit | grep -q . || {
    echo 'The package is missing third-party license texts.' >&2
    exit 1
}
output=$(cd "$work_dir" && ./codex_switch --version)
case "$output" in
    "$version ("*) ;;
    *) echo 'The package has an unexpected embedded version.' >&2; exit 1 ;;
esac
(cd "$work_dir" && ./codex_switch --help)
