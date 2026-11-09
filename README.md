# ccrotate

Automatic Claude Code account rotation for macOS.

ccrotate saves your Claude accounts and, when the active one reaches a usage
threshold, makes the next account with room the live login. Claude Code picks
up the new account without restarting. It does nothing else.

## Requirements

- macOS
- Claude Code, logged in with a Claude subscription
- Go 1.26.10 or newer
- [`just`](https://github.com/casey/just), when building from a clone

## Installation

```sh
go install github.com/faustbrian/ccrotate@latest
```

This installs into `$(go env GOPATH)/bin`, which must be on your `PATH`.

Or build from a clone, which installs into `~/.local/bin`:

```sh
git clone https://github.com/faustbrian/ccrotate.git
cd ccrotate
just install
```

## Usage

Save each account right after logging in to it. `add` saves whichever account
is logged in, so type `/login` inside Claude Code, finish the browser login as
the account you want, and wait for Claude Code to show that account:

```sh
ccrotate add work          # while logged in as the first account
# /login as the next account in Claude Code
ccrotate add personal
```

Then turn rotation on:

```sh
ccrotate on --5h 98 --7d 98
```

Both thresholds are percentages and default to 98. Run `ccrotate on` again to
change them.

| Command | What it does |
| --- | --- |
| `ccrotate add <name>` | Save the account currently logged in to Claude Code |
| `ccrotate rm <name>` | Forget a saved account |
| `ccrotate on [--5h N] [--7d N]` | Rotate at these usage percentages |
| `ccrotate off` | Stop rotating |

Do not use `/logout` between accounts, because it can revoke the login you just
saved. Names may use letters, digits and `. _ @ + -`.

Saved logins eventually expire. When one does, ccrotate skips that account and
logs why. Fix it with `/login` as that account, then `ccrotate add` with the
same name.

To see what it is doing:

```sh
tail -f ~/.ccrotate/ccrotate.log
```

## How it works

`ccrotate on` installs a launchd agent that runs a check every three minutes
and survives restarts. Each check reads the live account's usage and does
nothing while both windows are under their thresholds. When either window
reaches its threshold, ccrotate:

1. Walks the saved accounts in the order you added them, starting after the
   active one, and picks the first with room. If none has room, the login
   stays as it is.
2. Takes the same locks Claude Code uses for token refreshes.
3. Saves the outgoing account's newest tokens, because refresh tokens are
   single-use.
4. Replaces the login in the `Claude Code-credentials` Keychain item, keeping
   your MCP server logins, and updates `oauthAccount` in `~/.claude.json`.

Running sessions move to the new account when Claude Code's Keychain cache
expires, so a session can briefly stay on the old account. If you work heavily
enough to reach the real limit in that gap, use a lower threshold.

Tokens stay in the macOS Keychain under the `ccrotate` service. `~/.ccrotate`
holds only account names, account IDs, thresholds and the log.

## Limitations

- ccrotate relies on the private endpoints and Keychain format that Claude Code
  uses, so a Claude Code update can break it. Failed checks appear as `error:`
  lines in the log and leave your login alone.
- Do not run another account switcher at the same time. It would spend the
  refresh tokens ccrotate has saved.
- This project is not affiliated with Anthropic. Check that your use of several
  accounts complies with Anthropic's terms.

## Uninstalling

```sh
ccrotate off
ccrotate rm <name>         # for each saved account
just uninstall             # or remove the binary you installed
rm -rf ~/.ccrotate
```

## Development

```sh
just test
just lint
just check
```

The tests use a fake `security` binary, a fake API and a temporary home
directory, so they never touch your real Keychain or logins.

## License

MIT. See [LICENSE](LICENSE).
