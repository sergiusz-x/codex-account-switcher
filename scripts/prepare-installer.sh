#!/bin/sh
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
project_root=$(CDPATH= cd "$script_dir/.." && pwd)
asset_dir=${CODEX_SWITCH_RELEASE_ASSET_DIR:-"$project_root/release-assets"}
repository=${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY to OWNER/REPO.}
version=$(cat "$project_root/internal/version/VERSION")
case "$repository" in *[!A-Za-z0-9_./-]*) echo 'Invalid GitHub repository.' >&2; exit 1 ;; esac
case "$version" in *[!0-9.]*) echo 'Invalid release version.' >&2; exit 1 ;; esac
printf '%s\n' "$repository" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || {
    echo 'Invalid GitHub repository.' >&2
    exit 1
}
printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || {
    echo 'Invalid release version.' >&2
    exit 1
}
mkdir -p "$asset_dir"
license_work_dir=$(mktemp -d)
trap 'rm -rf -- "$license_work_dir"' EXIT
(cd "$project_root" && go run github.com/google/go-licenses/v2@v2.0.1 save ./cmd/codex_switch --save_path="$license_work_dir/third_party_licenses")
rm -rf -- "$asset_dir/third_party_licenses"
mv -- "$license_work_dir/third_party_licenses" "$asset_dir/third_party_licenses"
{
    printf '#!/bin/sh\nCODEX_SWITCH_REPOSITORY=%s\nCODEX_SWITCH_VERSION=%s\n' "$repository" "$version"
    cat "$project_root/install.sh"
} > "$asset_dir/install.sh"
