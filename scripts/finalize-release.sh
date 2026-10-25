#!/bin/sh
set -eu

version=${1:?Provide the release version.}
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
    echo 'Only the newest release tag can be published.' >&2
    exit 1
}

release_id=$(gh api "repos/$GITHUB_REPOSITORY/releases" --paginate \
    --jq ".[] | select(.tag_name == \"v$version\") | select(.draft) | .id")
[ -n "$release_id" ] || {
    echo 'Only a verified draft release can be published.' >&2
    exit 1
}
gh api "repos/$GITHUB_REPOSITORY/releases/$release_id" \
    --method PATCH --field draft=false --field make_latest=true >/dev/null
