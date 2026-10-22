# Codex Account Switcher

A small terminal UI for switching saved ChatGPT sign-ins used by Codex CLI.
Run `codex_switch` from any directory.

**Supported targets:** Ubuntu Linux `amd64` and Windows 11 `x64`. Codex CLI `0.163.0` with file-based ChatGPT sign-in is required.

Run `codex_switch --version` to see version and build information.

## Requirements and installation

Install or update the latest release with:

```sh
curl -fsSL https://raw.githubusercontent.com/sergiusz-x/codex-account-switcher/main/install.sh \
  | sh -s -- --repo sergiusz-x/codex-account-switcher
```

The installer downloads the latest Linux `amd64` release and verifies its
checksum before installation.

On Windows 11 x64, download the PowerShell installer from the latest release
and run it in Windows PowerShell 5.1 or PowerShell 7:

```powershell
$installer = Join-Path $env:TEMP 'codex-switch-install.ps1'
Invoke-WebRequest 'https://github.com/sergiusz-x/codex-account-switcher/releases/latest/download/install.ps1' -OutFile $installer
& $installer
Remove-Item -LiteralPath $installer
```

The Windows installer verifies the release checksum before installing or
updating `codex_switch.exe` in `%LOCALAPPDATA%\Programs\CodexSwitch\bin`.
Add that directory to your per-user `PATH` if you want to run `codex_switch`
from any terminal. It does not change `PATH` or install Codex CLI.
Use a normal terminal on Windows: Codex CLI does not support its shared daemon
in an elevated administrator session.

### Build from source

Building requires Go 1.26 or later. Codex CLI must be available in `PATH`.
Server integration targets Codex CLI `0.163.0` on Ubuntu with file-based
ChatGPT sign-in. Compatibility with every older or future CLI is not implied.

From the repository directory:

```sh
mkdir -p "$HOME/.local/bin"
go build -trimpath -o "$HOME/.local/bin/codex_switch" ./cmd/codex_switch
```

Run `codex_switch`. If your shell cannot find it, add `$HOME/.local/bin` to
`PATH` or use the executable's full path. Run `codex_switch --help` for options.

The default Codex home is `~/.codex`. Set `CODEX_HOME` or pass
`--codex-home /path/to/home` to use another directory. The explicit argument
wins over the environment variable.

## Versioning and development

Versions use SemVer and releases are automated from Conventional Commits; see
the [release process](docs/RELEASING.md). Release archives include the licenses
for the project and its Go dependencies.

## Saving accounts

Sign in to your first account through Codex, open `codex_switch`, and press
`s` to save the current login under a name such as `Personal`.

### Viewing usage

The main account list loads status automatically and shows each account's
email, plan, remaining quotas, reset times, and available reset count with the
time until the earliest reported reset expiry. Results
appear one account at a time without blocking account selection. Wide terminals
show columns; narrow terminals stack the same information beneath each name.
The list scrolls with the selection when all accounts do not fit.

Press Enter to switch, `r` to refresh all accounts, or `m` to open More.
Select Account usage details there to see full reset information and expiration
times for the selected account. Press `m` > Add account to start a separate
Codex login. Escape returns from account details to More. The summary includes available reset
counts and identifies partial coverage. Quota percentages are never added or
averaged across accounts because plans may have different capacities.

Reads use each saved login in a private temporary Codex home. The refresh token
is empty and tokens close to expiry are skipped. Results stay in memory for the
current TUI session and are never saved. Opening the switcher starts one read;
there is no background polling. Saving, adding, renewing, deleting, or switching
an account starts a new read. A status read does not switch the account, restart
Codex, or use a reset. Results are snapshots and may be partial; they do not
prove token validity or which account another Codex client has loaded.

### Adding an account

From More, choose Add account, enter a profile name, and confirm. The switcher temporarily leaves
its full-screen view and runs the standard `codex login --device-auth` command
in an empty private `CODEX_HOME`, with file-based credential storage. Open the
URL and enter the code shown by Codex in your browser. Choose the intended
ChatGPT account. Device code authorization may need to be enabled in ChatGPT
security settings. This is the supported headless login mechanism described in
[OpenAI's authentication documentation](https://learn.chatgpt.com/docs/auth).

After a successful login, the switcher imports the complete authentication file
and returns to the account list. Adding an account does not switch the current
login or restart its server. Select the new profile and press Enter to switch.
An existing identity or profile name is rejected. If the account is already
current, save it with `s` and use Renew login instead.

Ctrl+C cancels the Codex login. A ten-minute timeout also bounds the process.
Canceled or failed login commands do not import credentials. The private login
directory is removed after completion, including failure. A forced process kill
or machine crash can leave a private `codex-switch-login-*` directory in the
system temporary directory; treat it as credentials and remove it when the
switcher is no longer using it. Cleanup errors are reported.

### Renewing a saved login

Select the profile, press `m`, and choose **Renew login**. Confirm the explanation
and sign in through the same isolated flow. Use the same ChatGPT account and
workspace: both account ID and user subject must match. A different identity is
rejected without replacing the saved login or current authentication file.

For an inactive profile, renewal updates only its saved credentials and any
matching recovery copy. For the current profile, it preserves the outgoing
file, installs the new credentials in both `auth.json` and the profile, and
reloads a running managed Codex server. The confirmation explains that other
sessions may be interrupted. Loaded credentials are compared after restart;
a failed reload is reported as a partial result, not a successful switch.
A stopped server remains stopped. No automatic OAuth refresh is implemented.

Do not run `codex logout` or a new `codex login` against an existing sign-in to
add accounts. Codex `0.163.0` revokes that home's existing credentials before
login; a file copy cannot undo server-side revocation. The empty home used by
this flow contains none of the existing account credentials.
[Codex 0.163.0 login implementation](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/cli/src/login.rs).

Profiles live under `$CODEX_HOME/switcher`, or `~/.codex/switcher` by default.
Treat this directory like a password: it contains credentials. Keep it out of
repositories, cloud uploads, and issue attachments. On Ubuntu, owned directories
are private and credential files use `0600`. The profile index contains no tokens.

## Switching accounts

1. Select a profile with the arrow keys and press Enter.
2. If the shared Codex server needs to reload the account, finish work in other
   clients and confirm the restart. Other sessions can disconnect and their
   work can be interrupted. Canceling leaves the login unchanged.
3. The switcher stops the server, preserves the outgoing login including its
   final token update, installs the selected file, starts the server, and
   compares loaded credentials with the selected account's file.
4. Reopen disconnected Codex clients.

When no server is running, selecting a profile changes only the authentication
file. A later standard `codex` invocation starts its server with that file.
Selecting an account already loaded by the server preserves current tokens
without restarting the server.

The `(auth file)` marker describes the file. The result message distinguishes
file selection from a login confirmed as loaded by the server. Verification
does not check token validity with OpenAI: the switcher makes no test model
request and performs no custom OAuth refresh. Only an explicitly started add or renewal
flow invokes the standard Codex login and its authentication network requests.

If the current account is unknown, save it before switching. The `s` hint appears
only when a valid current account does not yet have a saved profile.

Press `m` for More actions on the selected profile: rename, delete, restore
the previous login, or renew its sign-in. More shows the profile ID. Deleting a saved profile leaves
the current Codex login unchanged. Press `q` to quit.

## Keeping saved credentials current

On startup and after TUI operations, the switcher matches the current file to
an existing profile using `account_id` and the ID token's `sub`. Changed contents
update that profile and display `Saved login updated from current auth.json.`.
The complete file is preserved, including unknown fields. Synchronization does
not replace active `auth.json`, create profiles for unknown accounts, or check
server-side token validity.

Synchronization preserves a new login after reauthentication; it cannot repair
a token already revoked by the server. Saved profiles are credential snapshots,
not a way to keep a revoked session valid.

## Recovery and limitations

Restore requires confirmation and coordinates with a running managed server.
A recognized current login is preserved first. Save an unknown current account
before recovery. Invalid current JSON is retained separately before restoring
a valid recovery copy.

A recent Codex CLI can reuse a background server after the terminal interface
exits. Closing and reopening the TUI alone does not guarantee reloaded credentials.
The switcher uses the CLI's lifecycle commands and a local WebSocket connection;
it does not kill arbitrary processes by name or change global configuration.
A server owned by another client, an incompatible protocol, or an unverifiable
account produces an error instead of an unverified success.

The local `getAuthStatus` request disables requested token refresh. Its secret
response is compared only in memory. Tokens and raw upstream errors are never
shown. Time limits bound server commands. After a partial failure, the message
states when the file has already changed; the switcher does not automatically
roll back to potentially stale credentials.

File validation does not reproduce Codex's complete managed configuration or
prove a token is still accepted remotely. Keyring, ephemeral login, API keys,
and architectures other than Windows x64 and Linux `amd64` are currently
unsupported. On Windows, use a local NTFS installation directory. Concurrent
Codex writes remain possible: the switcher lock is not respected by other
applications.

## Contributing

See [AGENTS.md](AGENTS.md) for development and testing requirements. The project
uses the [MIT License](LICENSE).
