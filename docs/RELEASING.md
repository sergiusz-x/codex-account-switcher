# Release process

## Automated releases

GitHub Actions verifies pull requests and pushes to `main`. Verification runs Go
formatting, vet and tests (including the race detector and an isolated Codex
server test with synthetic credentials), release-tool tests, ShellCheck, and
`actionlint`. A package job builds Linux `amd64` and Windows `amd64` previews
and checks both archives. A Windows runner tests the packaged Windows executable
and installer under Windows PowerShell 5.1 and PowerShell 7, then runs the
switcher against the pinned Codex npm launcher in a private synthetic home. The
Linux package also receives temporary-directory install and update tests. Pull
request previews expire after seven days.

GitHub-hosted Windows runners are elevated. Codex rejects shared-daemon startup
in that context, so these runners exercise real Codex status transport and skip
the daemon round-trip test. Before publishing Windows support, run
`SWITCHER_REAL_CODEX=1` with an absolute `SWITCHER_CODEX_BINARY` in a normal,
non-elevated Windows terminal and run `go test ./internal/switcher -run '^TestRealCodex' -count=1 -v`.
This uses only synthetic credentials and private temporary homes.

After verification and packaging pass on `main`, semantic-release analyzes
Conventional Commit messages. The first eligible release is `1.0.0`. It updates
`CHANGELOG.md` and `internal/version/VERSION`, commits those files, and creates a
`vMAJOR.MINOR.PATCH` tag. GoReleaser builds a draft containing both platform
archives, installers, and SHA-256 checksums. Linux package verification runs
before the draft is retained. A separate native Windows job downloads and tests
the exact draft assets, then exercises the tagged source with Codex. A final job
publishes the draft only after those Windows checks pass; a failed check leaves
the release as a draft for recovery.

| Commit type | Version change |
| --- | --- |
| `fix:` or `perf:` | Patch |
| `feat:` | Minor |
| `!` or `BREAKING CHANGE:` | Major |
| `docs:`, `test:`, or `chore:` | No release by default |

With squash merging, preserve the intended Conventional Commit type in the
resulting commit message. Do not manually edit the version or create release
tags; semantic-release owns both.

## Packages and installation

Packages target Linux `amd64` and Windows `amd64`, with baseline x86-64
instructions and no CGO requirement. Windows support requires Windows 11 and a
local NTFS destination. Windows PowerShell 5.1 and PowerShell 7 are covered by
the installer acceptance tests. Codex CLI `0.163.0` with file-based ChatGPT
sign-in is the supported runtime. Other operating systems, architectures, or
credential stores require implementation and separate validation before
packaging.

A release contains Linux `.tar.gz` and Windows `.zip` archives, `install.sh`,
`install.ps1`, and `checksums.txt`. The archives include the executable, project
license, and Go dependency license texts. Both installers verify the checksum
and executable before replacing the installed binary. The Windows installer
uses `%LOCALAPPDATA%\Programs\CodexSwitch\bin`; add that directory to the
per-user `PATH` to call `codex_switch` from any terminal. It requires Windows
11 x64, PowerShell 5.1 or later, and local NTFS storage. Neither installer
installs Codex CLI or edits shell configuration. Checksums detect accidental
corruption but are not independent signatures.

For a publicly readable repository, users install or update using the command
in the README. It downloads the installer from `main`, which fetches the latest
release and verifies its checksum before installation.

For an access-restricted repository, users need an authenticated GitHub CLI.
Download the installer asset from the latest release and run it with
`--github-cli`; this installs the release without cloning the source repository:

```sh
install_dir=$(mktemp -d)
trap 'rm -rf "$install_dir"' EXIT
gh release download --repo sergiusz-x/codex-account-switcher --pattern install.sh --dir "$install_dir"
sh "$install_dir/install.sh" --github-cli
```

The release installer pins its own version, so download the latest release's
installer again when updating. For public installations, use the command in the
README; no GitHub login is needed.

## GitHub permissions and recovery

Draft-build, draft-asset verification, and finalization jobs need `contents: write`;
GitHub does not expose draft release assets to read-only callers. Other verification
jobs use read-only permissions. The built-in `GITHUB_TOKEN` is sufficient when repository rules
allow the workflow to commit the release metadata and push tags. Actions are
pinned to commit SHAs, and the Node.js release dependencies are locked. CI does
not need Codex account credentials or other application secrets.

If a tag was created but publication failed, run the workflow manually with the
existing `release_tag` (for example, `v1.0.0`). Recovery accepts only the newest
version tag that belongs to `main`, reruns checks and packaging, and may replace
a draft release. The exact replacement draft must pass the native Windows gate
before finalization. Recovery refuses to replace a release that is already
public.
