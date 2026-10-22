# Release process

## Automated releases

GitHub Actions verifies pull requests and pushes to `main`. Verification runs Go
formatting, vet and tests (including the race detector and an isolated Codex
server test with synthetic credentials), release-tool tests, ShellCheck, and
`actionlint`. A package job builds a Linux `amd64` preview, checks the archive,
and tests installation and update in a temporary directory. Pull request previews
expire after seven days.

After verification and packaging pass on `main`, semantic-release analyzes
Conventional Commit messages. The first eligible release is `1.0.0`. It updates
`CHANGELOG.md` and `internal/version/VERSION`, commits those files, and creates a
`vMAJOR.MINOR.PATCH` tag. GoReleaser then builds the archive, installer, and
SHA-256 checksums. The publishing script verifies the package before publishing
the GitHub Release.

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

Current packages target Linux `amd64` with baseline x86-64 instructions
and no CGO requirement. Codex CLI `0.163.0` with file-based ChatGPT sign-in is
the supported runtime. Other operating systems, architectures, or credential
stores require implementation and separate validation before packaging.

A release contains a `.tar.gz` archive, `install.sh`, and `checksums.txt`. The
archive includes the executable, project license, and Go dependency license
texts. The installer verifies the checksum and executable before replacing the
installed binary. It installs or updates `codex_switch` in `~/.local/bin`; it does
not install Codex CLI or edit shell configuration. Checksums detect accidental
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

Only the release job needs `contents: write`; other jobs use read-only
permissions. The built-in `GITHUB_TOKEN` is sufficient when repository rules
allow the workflow to commit the release metadata and push tags. Actions are
pinned to commit SHAs, and the Node.js release dependencies are locked. CI does
not need Codex account credentials or other application secrets.

If a tag was created but publication failed, run the workflow manually with the
existing `release_tag` (for example, `v1.0.0`). Recovery accepts only the newest
version tag that belongs to `main`, reruns checks and packaging, and may replace
a draft release. It refuses to replace a release that is already public.
