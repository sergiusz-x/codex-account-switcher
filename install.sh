#!/bin/sh
# Install or update only the executable. Never read or change Codex credentials.
set -eu

repository=${CODEX_SWITCH_REPOSITORY:-}
version=${CODEX_SWITCH_VERSION:-}
bin_dir=${CODEX_SWITCH_BIN_DIR:-"$HOME/.local/bin"}
use_gh=false

fail() {
    printf 'codex_switch installer: %s\n' "$1" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --repo|--version|--bin-dir)
            [ "$#" -ge 2 ] || fail "Missing value for $1."
            case "$1" in
                --repo) repository=$2 ;;
                --version) version=${2#v} ;;
                --bin-dir) bin_dir=$2 ;;
            esac
            shift 2
            ;;
        --github-cli) use_gh=true; shift ;;
        --help)
            echo 'Usage: sh install.sh [--repo OWNER/REPO] [--version X.Y.Z] [--bin-dir PATH] [--github-cli]'
            exit 0
            ;;
        *) fail "Unknown argument: $1." ;;
    esac
done

[ "$(uname -s)" = Linux ] || fail 'This release supports Linux only.'
case "$(uname -m)" in
    x86_64|amd64) architecture=amd64 ;;
    *) fail 'This release supports x86-64 only.' ;;
esac
case "$repository" in *[!A-Za-z0-9_./-]*) fail 'Invalid GitHub repository.' ;; esac
printf '%s\n' "$repository" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' ||
    fail 'Provide a GitHub repository with --repo OWNER/REPO.'
if [ -n "$version" ]; then
    case "$version" in *[!0-9.]*) fail 'Version must use MAJOR.MINOR.PATCH.' ;; esac
    printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
        fail 'Version must use MAJOR.MINOR.PATCH.'
fi
[ -n "$bin_dir" ] || fail 'The installation directory cannot be empty.'
case "$bin_dir" in
    /*) ;;
    *) bin_dir=$PWD/$bin_dir ;;
esac
for tool in tar sha256sum mktemp install; do
    command -v "$tool" >/dev/null 2>&1 || fail "Required command is missing: $tool."
done
if [ "$use_gh" = true ]; then
    command -v gh >/dev/null 2>&1 || fail 'Install and authenticate GitHub CLI first.'
else
    command -v curl >/dev/null 2>&1 || fail 'Required command is missing: curl.'
fi

work_dir=$(mktemp -d)
pending=
cleanup() {
    if [ -n "$pending" ]; then rm -f -- "$pending"; fi
    rm -rf -- "$work_dir"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
archive=codex_switch_linux_${architecture}.tar.gz

if [ "$use_gh" = true ]; then
    if [ -n "$version" ]; then
        gh release download "v$version" --repo "$repository" --dir "$work_dir" \
            --pattern "$archive" --pattern checksums.txt
    else
        gh release download --repo "$repository" --dir "$work_dir" \
            --pattern "$archive" --pattern checksums.txt
    fi
else
    if [ -n "$version" ]; then
        base_url=https://github.com/$repository/releases/download/v$version
    else
        base_url=https://github.com/$repository/releases/latest/download
    fi
    for asset in "$archive" checksums.txt; do
        curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
            --connect-timeout 15 --max-time 120 --retry 2 \
            --output "$work_dir/$asset" "$base_url/$asset" ||
            fail 'Download failed. For a private repository, authenticate gh and use --github-cli.'
    done
fi

# Verify only the requested archive, without trusting paths from the manifest.
checksum=$(awk -v asset="$archive" '$2 == asset { print $1 }' "$work_dir/checksums.txt")
[ "${#checksum}" -eq 64 ] || fail 'Missing or invalid archive checksum.'
case "$checksum" in *[!0-9a-fA-F]*) fail 'Invalid archive checksum.' ;; esac
printf '%s  %s\n' "$checksum" "$archive" > "$work_dir/archive.sha256"
(cd "$work_dir" && sha256sum --check archive.sha256) || fail 'Archive checksum mismatch.'

# Extract only the regular executable, never archive paths or symlinks.
tar -xOzf "$work_dir/$archive" codex_switch > "$work_dir/codex_switch" ||
    fail 'The archive does not contain codex_switch.'
chmod 0755 "$work_dir/codex_switch"
installed_version=$("$work_dir/codex_switch" --version) || fail 'The downloaded executable failed its version check.'
if [ -n "$version" ]; then
    case "$installed_version" in
        "$version ("*) ;;
        *) fail 'The executable does not match the requested version.' ;;
    esac
fi
"$work_dir/codex_switch" --help >/dev/null 2>&1 || fail 'The downloaded executable failed its help check.'

mkdir -p -- "$bin_dir"
[ ! -d "$bin_dir/codex_switch" ] || fail 'The destination is a directory.'
[ ! -L "$bin_dir/codex_switch" ] || fail 'The destination is a symlink.'
pending=$(mktemp "$bin_dir/.codex_switch.XXXXXX")
install -m 0755 "$work_dir/codex_switch" "$pending"
mv -f -- "$pending" "$bin_dir/codex_switch"
pending=
printf 'Installed codex_switch %s\nLocation: %s/codex_switch\n' "$installed_version" "$bin_dir"
case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) printf 'Add this directory to PATH: %s\n' "$bin_dir" ;;
esac
