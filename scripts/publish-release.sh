#!/bin/sh
set -eu

version=${1:?Provide the release version.}
case "$version" in *[!0-9.]*) echo 'Invalid release version.' >&2; exit 1 ;; esac
printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || {
    echo 'Invalid release version.' >&2
    exit 1
}
[ "$(cat internal/version/VERSION)" = "$version" ] || {
    echo 'The release version does not match VERSION.' >&2
    exit 1
}
[ "$(git describe --tags --exact-match HEAD)" = "v$version" ] || {
    echo 'HEAD must be tagged with the release version.' >&2
    exit 1
}
latest_tag=$(git tag --list --sort=-version:refname 'v[0-9]*' | head -n 1)
[ "$latest_tag" = "v$version" ] || {
    echo 'Only the newest release tag can be published or recovered.' >&2
    exit 1
}

# Do not modify a release that users can already download.
state=$(gh api --paginate "repos/$GITHUB_REPOSITORY/releases?per_page=100" \
    --jq ".[] | select(.tag_name == \"v$version\") | .draft")
case "$state" in
    *false*) echo 'This version is already published; refusing to replace it.' >&2; exit 1 ;;
esac

# A failed build/upload leaves a draft. Retrying replaces only that draft.
goreleaser release --clean --release-notes CHANGELOG.md
sh scripts/check-package.sh "$version"
gh release edit "v$version" --repo "$GITHUB_REPOSITORY" --draft=false --latest
