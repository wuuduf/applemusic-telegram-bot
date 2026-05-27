#!/usr/bin/env python3
"""wmcli — wrapper-manager admin CLI.

A small interactive client for the WorldObservationLog/wrapper-manager gRPC
API. Implements the bits of the workflow that need a human in the loop
(login with 2FA, logout, status) without dragging in the full
AppleMusicDecrypt Python tree.

The Login bidi-stream protocol mirrors what wrapper-manager itself expects:

    client -> LoginRequest{username, password}
    server -> LoginReply{header.code = 2}            # 2FA required
    client -> LoginRequest{username, password, two_step_code}
    server -> LoginReply{header.code = 0}            # success
            | LoginReply{header.code = -1, msg=...}  # failure

Status / Logout are simple unary RPCs.

Subcommands:

    wmcli login                          # interactive multi-account loop
    wmcli login -u USER [-p PASS]        # single account
    wmcli login -f accounts.tsv          # batch from TSV (interactive 2FA)
    wmcli logout USER [USER ...]         # log out one or more accounts
    wmcli status                         # print ready / instances / regions

Environment:

    WRAPPER_MANAGER_ADDR   default for --manager (defaults to localhost:8080)
    APPLE_PASSWORD         fallback password when -p is omitted in single-shot
"""

from __future__ import annotations

import argparse
import asyncio
import getpass
import os
import sys
from typing import Awaitable, Callable, Iterable, List, Optional, Tuple

import grpc
from google.protobuf import empty_pb2

import manager_pb2 as pb
import manager_pb2_grpc as pb_grpc


# ---------------------------------------------------------------------------
# Account collection helpers
# ---------------------------------------------------------------------------

Account = Tuple[str, str, Optional[str]]
"""(username, password, optional_static_two_step_code)"""


def _read_accounts_file(path: str) -> List[Account]:
    """Parse a TSV/whitespace file: ``user<TAB>pass [<TAB>two_step_code]``.

    Lines starting with ``#`` and blank lines are skipped. The third column
    is intentionally rare and only useful for fully-automated tests; real
    Apple 2FA codes are time-limited and meant to be entered interactively.
    """
    accounts: List[Account] = []
    with open(path, "r", encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            # Prefer TAB as the separator; fall back to any whitespace so
            # hand-edited files still work.
            parts = line.split("\t")
            if len(parts) < 2:
                parts = line.split(None, 2)
            if len(parts) < 2:
                print(f"[warn] skipping {path}:{lineno}: expected user<TAB>password",
                      file=sys.stderr)
                continue
            username, password = parts[0], parts[1]
            two_step = parts[2] if len(parts) >= 3 and parts[2] else None
            accounts.append((username, password, two_step))
    return accounts


def _prompt_accounts_interactive() -> List[Account]:
    """Loop, asking for username + (hidden) password until empty input."""
    accounts: List[Account] = []
    print("Enter Apple ID accounts to log in (empty username to finish).")
    while True:
        try:
            username = input("AppleID: ").strip()
        except EOFError:
            break
        if not username:
            break
        try:
            password = getpass.getpass(f"Password for {username}: ")
        except EOFError:
            print("[abort] no password provided", file=sys.stderr)
            return []
        if not password:
            print(f"[skip] empty password for {username}", file=sys.stderr)
            continue
        accounts.append((username, password, None))
    return accounts


# ---------------------------------------------------------------------------
# Login bidi-stream driver
# ---------------------------------------------------------------------------


class _LoginAborted(Exception):
    """Raised internally when the login flow can't continue."""


async def _drive_login(
    stub: pb_grpc.WrapperManagerServiceStub,
    username: str,
    password: str,
    two_step_provider: Callable[[str], Awaitable[str]],
) -> Tuple[bool, str]:
    """Run a single Login bidi stream session for one account.

    Returns ``(ok, message)`` instead of raising so the caller can keep
    iterating through a batch even if one account fails.
    """
    request_queue: "asyncio.Queue[Optional[pb.LoginRequest]]" = asyncio.Queue()

    async def request_iter():
        # The stream stays open as long as we keep yielding. ``None`` is the
        # sentinel that tells the server we're done sending and lets gRPC
        # half-close the stream cleanly.
        while True:
            item = await request_queue.get()
            if item is None:
                return
            yield item

    # First request: just username + password. wrapper-manager will spawn a
    # wrapper instance and start the auth dance; we wait for its reply to
    # decide whether to send a 2FA code or stop.
    await request_queue.put(pb.LoginRequest(
        data=pb.LoginData(username=username, password=password)))

    call = stub.Login(request_iter())

    try:
        async for reply in call:
            code = reply.header.code
            msg = reply.header.msg or ""
            if code == 0:
                await request_queue.put(None)
                return True, msg or "login success"
            if code == -1:
                await request_queue.put(None)
                return False, msg or "login failed"
            if code == 2:
                # 2FA prompt. The provider may block on stdin; that's fine —
                # we always process accounts sequentially, so blocking the
                # event loop here doesn't starve anything else.
                try:
                    two_step_code = await two_step_provider(username)
                except _LoginAborted as exc:
                    await request_queue.put(None)
                    return False, str(exc) or "2FA aborted"
                if not two_step_code:
                    await request_queue.put(None)
                    return False, "empty 2FA code"
                await request_queue.put(pb.LoginRequest(
                    data=pb.LoginData(
                        username=username,
                        password=password,
                        two_step_code=two_step_code,
                    )
                ))
            else:
                # Unknown intermediate code — log and keep listening so a
                # future server version that introduces new states (e.g. a
                # progress code) doesn't break this client.
                print(f"[warn] {username}: unexpected reply code={code} msg={msg}",
                      file=sys.stderr)
    except grpc.aio.AioRpcError as exc:
        return False, f"rpc error: {exc.code().name}: {exc.details()}"
    except asyncio.CancelledError:
        await request_queue.put(None)
        raise

    # Stream ended without a terminal code (server hung up). Treat as
    # failure so the operator sees something went wrong.
    return False, "login stream ended without terminal reply"


def _make_two_step_provider(static_code: Optional[str]) -> Callable[[str], Awaitable[str]]:
    """Return an async callable that yields a 2FA code on demand.

    If ``static_code`` is set we just return it once; otherwise we prompt
    on stdin (with the prompt going to stderr so it stays visible even when
    stdout is being piped).
    """
    used = False

    async def provider(username: str) -> str:
        nonlocal used
        if static_code and not used:
            used = True
            return static_code
        # Prompt synchronously. This blocks the event loop, which is fine
        # because we only ever drive one login at a time.
        try:
            print(f"\n>>> {username}: enter 2FA code (Apple should have just texted/pushed it)",
                  file=sys.stderr, flush=True)
            return input("2FA code: ").strip()
        except EOFError:
            raise _LoginAborted("no 2FA code on stdin (EOF)")

    return provider


# ---------------------------------------------------------------------------
# Subcommand handlers
# ---------------------------------------------------------------------------


async def _open_channel(addr: str) -> grpc.aio.Channel:
    return grpc.aio.insecure_channel(addr)


async def _print_status(stub: pb_grpc.WrapperManagerServiceStub) -> pb.StatusData:
    resp: pb.StatusReply = await stub.Status(empty_pb2.Empty())
    if resp.header.code != 0:
        raise SystemExit(f"status rpc failed: code={resp.header.code} msg={resp.header.msg}")
    data = resp.data
    print(f"  ready          : {data.ready}")
    print(f"  status         : {data.status}")
    print(f"  instance_count : {data.client_count}")
    print(f"  regions        : {list(data.regions) or '[]'}")
    return data


async def cmd_status(args: argparse.Namespace) -> int:
    channel = await _open_channel(args.manager)
    try:
        stub = pb_grpc.WrapperManagerServiceStub(channel)
        await _print_status(stub)
    finally:
        await channel.close()
    return 0


async def cmd_login(args: argparse.Namespace) -> int:
    accounts = _collect_accounts_for_login(args)
    if not accounts:
        print("[error] no accounts to log in", file=sys.stderr)
        return 2

    channel = await _open_channel(args.manager)
    try:
        stub = pb_grpc.WrapperManagerServiceStub(channel)

        # Quick reachability probe so we fail fast with a clear error
        # instead of silently hanging on the first Login call.
        try:
            await asyncio.wait_for(_print_status(stub), timeout=5.0)
        except asyncio.TimeoutError:
            print(f"[error] {args.manager}: status rpc timed out (manager unreachable?)",
                  file=sys.stderr)
            return 3
        except grpc.aio.AioRpcError as exc:
            print(f"[error] {args.manager}: {exc.code().name}: {exc.details()}",
                  file=sys.stderr)
            return 3

        results: List[Tuple[str, bool, str]] = []
        for username, password, static_two_step in accounts:
            print(f"\n=== logging in {username} ===")
            provider = _make_two_step_provider(static_two_step)
            ok, message = await _drive_login(stub, username, password, provider)
            tag = "OK  " if ok else "FAIL"
            print(f"[{tag}] {username}: {message}")
            results.append((username, ok, message))

        print("\n=== summary ===")
        for username, ok, message in results:
            tag = "OK  " if ok else "FAIL"
            print(f"  [{tag}] {username}: {message}")

        # Show the post-login account count so the operator gets immediate
        # confirmation that wrapper-manager picked the new instances up.
        try:
            print("\n=== status after login ===")
            await _print_status(stub)
        except Exception as exc:  # noqa: BLE001 - best effort
            print(f"[warn] post-login status check failed: {exc}", file=sys.stderr)

        any_failure = any(not ok for _, ok, _ in results)
        return 1 if any_failure else 0
    finally:
        await channel.close()


def _collect_accounts_for_login(args: argparse.Namespace) -> List[Account]:
    if args.from_file:
        accounts = _read_accounts_file(args.from_file)
        if not accounts:
            print(f"[error] {args.from_file}: no usable accounts", file=sys.stderr)
        return accounts
    if args.username:
        password = (
            args.password
            or os.environ.get("APPLE_PASSWORD")
            or getpass.getpass(f"Password for {args.username}: ")
        )
        if not password:
            print("[error] empty password", file=sys.stderr)
            return []
        return [(args.username, password, None)]
    return _prompt_accounts_interactive()


async def cmd_logout(args: argparse.Namespace) -> int:
    channel = await _open_channel(args.manager)
    failures = 0
    try:
        stub = pb_grpc.WrapperManagerServiceStub(channel)
        for username in args.usernames:
            try:
                resp: pb.LogoutReply = await stub.Logout(
                    pb.LogoutRequest(data=pb.LogoutData(username=username))
                )
                if resp.header.code == 0:
                    print(f"[OK  ] {username} logged out")
                else:
                    failures += 1
                    print(f"[FAIL] {username}: {resp.header.msg}")
            except grpc.aio.AioRpcError as exc:
                failures += 1
                print(f"[FAIL] {username}: {exc.code().name}: {exc.details()}")
    finally:
        await channel.close()
    return 0 if failures == 0 else 1


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="wmcli",
        description="wrapper-manager admin CLI (login/logout/status)",
    )
    parser.add_argument(
        "--manager",
        default=os.environ.get("WRAPPER_MANAGER_ADDR", "localhost:8080"),
        help="gRPC address of wrapper-manager (default: $WRAPPER_MANAGER_ADDR or localhost:8080)",
    )
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_status = sub.add_parser("status", help="show readiness / instance count / regions")
    p_status.set_defaults(func=cmd_status)

    p_login = sub.add_parser(
        "login",
        help="log in one or more Apple IDs (interactive 2FA)",
        description=(
            "Without arguments, prompts for usernames + passwords until you "
            "press ENTER on an empty AppleID line. With -u/-p, logs in a "
            "single account. With -f, reads tab-separated user/pass pairs "
            "from a file (2FA still entered interactively per account)."
        ),
    )
    p_login.add_argument("-u", "--username", help="single account username (AppleID)")
    p_login.add_argument(
        "-p", "--password",
        help="password for -u (or set $APPLE_PASSWORD; otherwise prompted)",
    )
    p_login.add_argument(
        "-f", "--from-file",
        help="path to a TSV/whitespace file: 'user<TAB>password' (one per line)",
    )
    p_login.set_defaults(func=cmd_login)

    p_logout = sub.add_parser("logout", help="log out one or more Apple IDs by username")
    p_logout.add_argument("usernames", nargs="+", metavar="USER")
    p_logout.set_defaults(func=cmd_logout)

    return parser


def main(argv: Optional[Iterable[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(list(argv) if argv is not None else None)
    try:
        return asyncio.run(args.func(args))
    except KeyboardInterrupt:
        print("\n[abort] interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main())
