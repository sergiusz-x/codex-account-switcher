#!/bin/sh
set -eu

version=${1:?Provide the expected embedded version.}
archive=codex_switch_linux_amd64.tar.gz
windows_archive=codex_switch_windows_amd64.zip
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT
trap 'exit 1' HUP INT TERM
cp dist/*.tar.gz dist/*.zip dist/checksums.txt release-assets/install.sh release-assets/install.ps1 "$work_dir/"
(cd "$work_dir" && sha256sum --check checksums.txt)
package_dir=$work_dir/linux-package
mkdir "$package_dir"
tar -xzf "$work_dir/$archive" -C "$package_dir"
for file in README.md CHANGELOG.md LICENSE; do
    test -f "$package_dir/$file"
done
find "$package_dir/release-assets/third_party_licenses" -type f -print -quit | grep -q . || {
    echo 'The package is missing third-party license texts.' >&2
    exit 1
}
output=$(cd "$package_dir" && ./codex_switch --version)
case "$output" in
    "$version ("*) ;;
    *) echo 'The package has an unexpected embedded version.' >&2; exit 1 ;;
esac
(cd "$package_dir" && ./codex_switch --help)
python3 - "$work_dir/$windows_archive" "$work_dir/codex_switch.exe" <<'PY'
import pathlib
import stat
import sys
import zipfile

archive = pathlib.Path(sys.argv[1])
output = pathlib.Path(sys.argv[2])
with zipfile.ZipFile(archive) as package:
    names = package.namelist()
    if names.count("codex_switch.exe") != 1:
        raise SystemExit("The Windows package must contain exactly one root codex_switch.exe.")
    required = {"README.md", "CHANGELOG.md", "LICENSE"}
    missing = required.difference(names)
    if missing:
        raise SystemExit("The Windows package is missing documentation or the project license.")
    license_prefix = "release-assets/third_party_licenses/"
    if not any(name.startswith(license_prefix) and not name.endswith("/") for name in names):
        raise SystemExit("The Windows package is missing third-party license texts.")
    for name in names:
        path = pathlib.PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or "\\" in name or ":" in name:
            raise SystemExit("The Windows package contains an unsafe ZIP path.")
        mode = package.getinfo(name).external_attr >> 16
        if stat.S_ISLNK(mode) or mode and not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
            raise SystemExit("The Windows package contains a link or special ZIP entry.")
    output.write_bytes(package.read("codex_switch.exe"))

if output.stat().st_size <= 0 or output.read_bytes()[:2] != b"MZ":
    raise SystemExit("The Windows package does not contain a PE executable.")
PY
grep -Fq "\$script:DefaultRepository = '$GITHUB_REPOSITORY'" "$work_dir/install.ps1" || {
    echo 'The PowerShell installer is not pinned to this repository.' >&2
    exit 1
}
grep -Fq "\$script:DefaultVersion = '$version'" "$work_dir/install.ps1" || {
    echo 'The PowerShell installer is not pinned to this version.' >&2
    exit 1
}
