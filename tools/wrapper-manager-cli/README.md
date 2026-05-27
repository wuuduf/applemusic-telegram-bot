# wmcli — wrapper-manager admin CLI

A small, container-friendly command-line tool that drives
[`WorldObservationLog/wrapper-manager`](https://github.com/WorldObservationLog/wrapper-manager)
through the operator-facing parts of its gRPC API: **login** (with 2FA),
**logout**, and **status**.

It exists so this repository's Make targets (`make login` / `make
login-batch` / `make logout` / `make accounts`) can manage Apple IDs in
the running `wrapper-manager` container without you having to clone
`AppleMusicDecrypt` or set up a Python virtualenv on the host.

## What it talks to

```
wmcli (this tool, in a container) ──gRPC──▶ wrapper-manager:8080
```

`wrapper-shim` and the bot itself never touch this tool. It is purely an
admin/data-plane bootstrapper. After accounts are logged in, `wrapper-manager`
persists them under `./wrapper-manager-data/` and the bot can use them via
`wrapper-shim`.

## Subcommands

| Subcommand                            | What it does                                             |
| ------------------------------------- | -------------------------------------------------------- |
| `wmcli status`                        | Print `Ready / status / instance_count / regions`        |
| `wmcli login`                         | Prompt for AppleIDs/passwords until empty input; 2FA per account |
| `wmcli login -u USER [-p PASS]`       | Log in one specific account                              |
| `wmcli login -f accounts.tsv`         | Batch login from a tab-separated `user<TAB>pass` file    |
| `wmcli logout USER [USER ...]`        | Log out one or more accounts by AppleID                  |

The `--manager HOST:PORT` flag (or the `WRAPPER_MANAGER_ADDR` env var)
controls which wrapper-manager instance to talk to. When invoked through
the repo's `Makefile`, this is automatically wired to the running
`wrapper-manager` container via `--network container:wrapper-manager`,
so `localhost:8080` always resolves correctly.

## Login state machine

Sourced from `WorldObservationLog/AppleMusicDecrypt/src/grpc/manager.py`,
verified against `wrapper-manager/handler.go`:

```
client                                server
------                                ------
LoginRequest{user, pass}        ───▶
                                ◀───  LoginReply{code = 2}     (need 2FA)
LoginRequest{user, pass, code}  ───▶
                                ◀───  LoginReply{code = 0}     (success)
                                  or  LoginReply{code = -1}    (failure)
```

`code == 2` is the only intermediate state; any unknown code is logged
and the loop keeps listening so future server changes don't crash this
client.

## Building (manual)

You don't normally need to build this manually — `make wmcli-build` does
it for you. But if you want to:

```sh
docker build -t wmcli:local -f tools/wrapper-manager-cli/Dockerfile .
```

The Dockerfile context **must** be the repo root because it pulls the
proto file from `wrapper-shim/proto/manager.proto` so there's a single
source of truth for the protocol definition.

## Running outside the project's docker compose

If your wrapper-manager is reachable on the host at, say, `127.0.0.1:8080`:

```sh
docker run --rm -it --network=host wmcli:local \
    --manager 127.0.0.1:8080 \
    login
```

Or against a remote manager:

```sh
docker run --rm -it wmcli:local \
    --manager wm.internal.example.com:8080 \
    login -u me@example.com
```

## Batch file format

```
# comments allowed
alice@example.com	correct-horse-battery-staple
bob@example.com 	password-with-spaces-also-ok
```

* Tab-separated by default; whitespace also accepted as a fallback so
  hand-edited files don't fight you.
* 2FA codes are *not* read from the file — they're always prompted on
  stdin so the tool can collect each fresh code as Apple delivers it.
* `chmod 600` the file. It contains plaintext passwords.

## Tests

`test_wmcli.py` is a stdlib-only smoke test that mocks the gRPC surface
and drives `_drive_login` through every documented reply code. Run it
without installing anything:

```sh
python3 tools/wrapper-manager-cli/test_wmcli.py
# or
make wmcli-test
```

It covers: happy path (code 0), 2FA round-trip (code 2 → 0), terminal
failure (code -1), unknown intermediate codes being skipped, premature
stream closure, and an empty 2FA prompt aborting cleanly. It also
exercises the TSV parser (tabs, whitespace fallback, comments) and the
argparse subcommands.
```
