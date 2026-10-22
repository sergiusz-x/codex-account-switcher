# Codex Account Switcher contributor and coding-agent instructions

## Purpose and scope

- Build a small, fast, stable TUI for switching local Codex CLI sign-ins.
- Prioritize safe file operations, simple code, fast startup, and low resource use.
- The supported release target is Linux `amd64`; see `docs/RELEASING.md`.
- The application includes read-only account status alongside the switcher.
- Never use a developer's real Codex sign-in during development or tests. Use synthetic credentials and isolated temporary homes; maintainers perform any real-account acceptance checks.

## Language and simplicity

- Write all repository content in English: documentation, agent instructions, source code, comments, identifiers, UI text, errors, logs, tests, examples, and release notes.
- Write simple, idiomatic Go. Follow KISS, DRY, and YAGNI; prefer the standard library and small functions with concrete names.
- Add dependencies only for a demonstrated need. Bubble Tea and Lip Gloss implement the TUI; `coder/websocket` implements the existing Codex server transport.
- Do not add DI frameworks, generic repository layers, plugin systems, a database, or an application server or daemon.
- Do not introduce an interface for a single implementation unless a specific I/O failure test requires it.
- Avoid speculative features and dead options. Keep profile and file operations separate from rendering without creating a package per function.
- Handle errors explicitly, including write, close, sync, replacement, and process cleanup errors.

## Credentials

- Treat `auth.json`, profile copies, temporary files, recovery files, and token-bearing protocol responses as secrets.
- Never place real tokens in the repository, logs, errors, screenshots, tests, or responses.
- Do not display real `auth.json` contents during diagnostics. Report only necessary structural information.
- Use synthetic credentials and temporary directories for tests and experiments. Never use the real `~/.codex` in tests.
- Preserve the complete original authentication file, including unknown fields. Parse only what validation and identity matching require.
- On Ubuntu, write secrets with `0600` permissions and owned directories with `0700` permissions.
- Before switching, preserve the current file and its refreshed tokens. Never choose the destination profile from a cached active marker alone.
- Validate the target and prepare recovery data before replacing the active file.
- Never use `codex logout` to switch accounts or implement custom OAuth refresh logic in the MVP.
- Do not change global `config.toml`, account settings, or credential storage as a side effect.
- Usage refresh may run `codex app-server` in a private temporary `CODEX_HOME` with the saved ID token, access token, account ID, and `last_refresh` needed to load managed ChatGPT auth. Store an empty refresh token, and reject an access token expiring within 10 minutes so Codex cannot rotate credentials. Strip base-URL, refresh-URL, and proxy environment overrides. Never persist status results; remove the temporary home after the request.

## Application behavior

- Distinguish selecting an authentication file from confirming the credentials loaded by the Codex server. Neither proves server-side token validity.
- Stop and restart a running shared server only after TUI confirmation explaining that other sessions may be interrupted. Never restart a developer's real server during development or tests.
- Require a successful stop acknowledgment before replacing the file. After startup, compare loaded credentials with the selected account's file. Failed verification is not a successful switch.
- Use the local transport without requesting token refresh. Compare token-bearing responses only in memory; never send them to UI, errors, or logs.
- The switcher lock coordinates only our processes. Do not claim that Codex respects it.
- Do not add external network requests, telemetry, background polling, or automatic account selection. Network access for account addition, renewal, and account status is limited to standard Codex commands. Status loads once on startup and after account changes, or explicitly with `r`; never poll. Keep selection responsive, show results progressively, and cancel reads before switching or starting login.
- Run account addition and renewal in an empty private temporary Codex home with file storage. Never seed it with existing credentials. Require a matching identity for renewal, bound the login process, and remove temporary credentials after success or failure.
- Do not base shared application logic on symlinks, Bash, or POSIX permission assumptions.
- Do not claim Windows support from compilation alone. Replacement, locking, ACLs, and server transport require separate validation.
- Deleting a profile removes its saved credentials without changing active `auth.json` or signing Codex out.

## Quality and Git

- Keep changes focused. Avoid unrelated refactoring.
- Format with `gofmt`. Run `go vet ./...` and `go test ./...` after Go changes. Run `npm test`, ShellCheck, and actionlint when changing release automation or shell scripts.
- Test data loss, refreshed tokens, write failures, interrupted operations, concurrency, server failures, and partial results. Avoid tests that merely repeat trivial implementation details.
- Keep real-Codex integration tests explicitly opt-in and isolated to synthetic credentials and temporary homes.
- Keep CI focused on the CLI, its installer, and release automation. Do not add local hooks or unrelated quality tools.
- Before committing, review the file list and diff for secrets. Never force-add authentication files.
- Do not configure a developer's Git identity, rewrite commits, push changes, change remotes, or change repository visibility unless explicitly requested.

## Versioning

- `internal/version/VERSION` is the sole application version source; `--version` reads it.
- Use plain SemVer `MAJOR.MINOR.PATCH`. Do not increment the version for each rebuild.
- Use Conventional Commits. `fix:` and `perf:` release patches, `feat:` releases features, and `!` or `BREAKING CHANGE:` releases incompatible changes. Documentation and maintenance commits do not release by default.
- CI runs semantic-release on `main` after verification and package checks. It updates `CHANGELOG.md` and `internal/version/VERSION`, commits them, and creates `vMAJOR.MINOR.PATCH`. Do not manually bump versions or create release tags during normal development.
- The first automatic release is `1.0.0`. Between releases, keep the last release version in `VERSION`; source revision metadata distinguishes development builds.
- Increment major for incompatible changes, minor for compatible features, and patch for compatible fixes.
- GoReleaser packages only platforms that have working implementations and empirical validation. Currently publish Linux `amd64` for the Ubuntu MVP.
- See `docs/RELEASING.md` for the release workflow, supported targets, and release verification requirements.
