"""Stdlib-only smoke test for wmcli's login state machine and TSV parser.

This file deliberately avoids importing grpcio / google.protobuf so it can
run in any Python 3.9+ environment with no extra setup. It mocks the gRPC
surface that wmcli touches and drives the login bidi-stream state machine
through every reply code documented in
https://github.com/WorldObservationLog/AppleMusicDecrypt/blob/main/src/grpc/manager.py:

    code  0 -> success
    code -1 -> terminal failure
    code  2 -> 2FA required (server expects one more LoginRequest with
                              two_step_code populated)

Run from the repo root:

    python3 tools/wrapper-manager-cli/test_wmcli.py

It also runs as part of `make wmcli-test`.
"""

from __future__ import annotations

import asyncio
import os
import sys
import tempfile
import types
from types import SimpleNamespace
from typing import List


# ---------------------------------------------------------------------------
# Stub injection — has to happen before `import wmcli`.
# ---------------------------------------------------------------------------


class _AioRpcError(Exception):
    def code(self):
        return SimpleNamespace(name="UNKNOWN")

    def details(self):
        return "fake"


def _install_stubs() -> None:
    grpc_mod = types.ModuleType("grpc")
    aio_mod = types.ModuleType("grpc.aio")
    aio_mod.AioRpcError = _AioRpcError
    aio_mod.insecure_channel = lambda *a, **k: None
    grpc_mod.aio = aio_mod
    sys.modules["grpc"] = grpc_mod
    sys.modules["grpc.aio"] = aio_mod

    google_mod = types.ModuleType("google")
    protobuf_mod = types.ModuleType("google.protobuf")
    empty_mod = types.ModuleType("google.protobuf.empty_pb2")
    empty_mod.Empty = type("Empty", (), {})
    google_mod.protobuf = protobuf_mod
    sys.modules["google"] = google_mod
    sys.modules["google.protobuf"] = protobuf_mod
    sys.modules["google.protobuf.empty_pb2"] = empty_mod

    pb = types.ModuleType("manager_pb2")

    class _Bag:
        def __init__(self, **kw):
            self.__dict__.update(kw)

    pb.LoginRequest = _Bag
    pb.LoginData = _Bag
    pb.LogoutRequest = _Bag
    pb.LogoutData = _Bag
    pb.LoginReply = _Bag
    pb.LogoutReply = _Bag
    pb.StatusReply = _Bag
    sys.modules["manager_pb2"] = pb

    pbg = types.ModuleType("manager_pb2_grpc")
    pbg.WrapperManagerServiceStub = type("Stub", (), {})
    sys.modules["manager_pb2_grpc"] = pbg


_install_stubs()
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import wmcli  # noqa: E402  (deliberate: must import after stub injection)


# ---------------------------------------------------------------------------
# Tiny fake gRPC stub so we can drive _drive_login without real networking.
# ---------------------------------------------------------------------------


def _reply(code: int, msg: str = "") -> SimpleNamespace:
    """Build a LoginReply-shaped object: reply.header.code / reply.header.msg."""
    return SimpleNamespace(header=SimpleNamespace(code=code, msg=msg))


class _FakeLoginCall:
    """Async iterator over canned replies, with a parallel drainer that
    captures every request the client sends. Replies and requests must
    flow independently in real gRPC bidi streams (server can fire multiple
    replies without the client sending anything new), so we model them as
    two decoupled pumps here.
    """

    def __init__(self, replies: List[SimpleNamespace], request_iter):
        self._replies = list(replies)
        self.received: List[object] = []
        # Drainer task pulls every item the client puts on the request
        # iterator. It terminates when the client signals end-of-stream
        # (yields nothing further), which wmcli does by pushing None into
        # its internal queue.
        self._drainer = asyncio.create_task(self._drain(request_iter))

    async def _drain(self, request_iter):
        try:
            async for req in request_iter:
                self.received.append(req)
        except (StopAsyncIteration, asyncio.CancelledError):
            pass

    def __aiter__(self):
        return self

    async def __anext__(self):
        # Yield once so any request the client is in the middle of pushing
        # gets recorded by the drainer before we deliver the next reply;
        # this keeps reply n logically "after" any request n.
        await asyncio.sleep(0)
        if not self._replies:
            # No more canned replies — close the stream. The drainer will
            # finish on its own once the client half-closes via `None`.
            raise StopAsyncIteration
        return self._replies.pop(0)

    async def aclose(self):
        # Give the drainer a beat to record trailing sends, then cancel.
        try:
            await asyncio.wait_for(self._drainer, timeout=0.5)
        except asyncio.TimeoutError:
            self._drainer.cancel()


class _FakeStub:
    def __init__(self, replies):
        self._replies = list(replies)
        self.last_call: "_FakeLoginCall | None" = None

    def Login(self, request_iter):
        # request_iter is the async generator returned by wmcli; we wrap it
        # so we can inspect what the client sent on the wire.
        self.last_call = _FakeLoginCall(self._replies, request_iter)
        return self.last_call


# ---------------------------------------------------------------------------
# Tests — login state machine
# ---------------------------------------------------------------------------


async def t_success_no_2fa():
    stub = _FakeStub([_reply(0, "welcome")])

    async def two_step(_u: str) -> str:
        raise AssertionError("must not be called when 2FA is not requested")

    ok, msg = await wmcli._drive_login(stub, "alice@x", "pw", two_step)
    await stub.last_call.aclose()
    assert ok is True, msg
    assert "welcome" in msg, msg
    assert len(stub.last_call.received) == 1, stub.last_call.received
    sent = stub.last_call.received[0]
    assert sent.data.username == "alice@x"
    assert sent.data.password == "pw"
    assert getattr(sent.data, "two_step_code", "") == ""


async def t_success_with_2fa():
    stub = _FakeStub([_reply(2, "need code"), _reply(0, "ok")])

    async def two_step(username: str) -> str:
        assert username == "bob@y"
        return "654321"

    ok, msg = await wmcli._drive_login(stub, "bob@y", "pw2", two_step)
    await stub.last_call.aclose()
    assert ok is True, msg
    rec = stub.last_call.received
    assert len(rec) == 2, rec
    assert getattr(rec[0].data, "two_step_code", "") == ""
    assert rec[1].data.two_step_code == "654321"


async def t_failure_terminal():
    stub = _FakeStub([_reply(-1, "wrong password")])

    async def two_step(_u: str) -> str:
        raise AssertionError("not reached")

    ok, msg = await wmcli._drive_login(stub, "u", "bad", two_step)
    await stub.last_call.aclose()
    assert ok is False
    assert "wrong password" in msg, msg


async def t_unknown_intermediate_code_is_ignored():
    # Server emits an unknown progress-style code, then settles on success.
    stub = _FakeStub([_reply(7, "in progress"), _reply(0, "done")])

    async def two_step(_u: str) -> str:
        raise AssertionError("not reached")

    ok, msg = await wmcli._drive_login(stub, "u", "p", two_step)
    await stub.last_call.aclose()
    assert ok is True, msg
    assert len(stub.last_call.received) == 1


async def t_stream_closes_without_terminal():
    stub = _FakeStub([])  # server hangs up immediately

    async def two_step(_u: str) -> str:
        raise AssertionError("not reached")

    ok, msg = await wmcli._drive_login(stub, "u", "p", two_step)
    await stub.last_call.aclose()
    assert ok is False
    assert "without terminal" in msg, msg


async def t_empty_2fa_aborts():
    stub = _FakeStub([_reply(2, "need code")])

    async def two_step(_u: str) -> str:
        return ""  # operator pressed Enter on the prompt

    ok, msg = await wmcli._drive_login(stub, "u", "p", two_step)
    await stub.last_call.aclose()
    assert ok is False
    assert "empty 2FA code" in msg, msg


# ---------------------------------------------------------------------------
# Tests — TSV parser + argparse
# ---------------------------------------------------------------------------


def t_tsv_parser():
    body = (
        "# header comment\n"
        "\n"
        "alice@x.com\tpw1\n"
        "bob@y.com\tpw with spaces\n"
        "carol@z.com pw3 staticcode\n"  # whitespace fallback with optional 3rd col
    )
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False) as fh:
        fh.write(body)
        path = fh.name
    try:
        accs = wmcli._read_accounts_file(path)
    finally:
        os.unlink(path)
    assert len(accs) == 3, accs
    assert accs[0] == ("alice@x.com", "pw1", None), accs[0]
    assert accs[1] == ("bob@y.com", "pw with spaces", None), accs[1]
    assert accs[2] == ("carol@z.com", "pw3", "staticcode"), accs[2]


def t_argparse_smoke():
    parser = wmcli._build_parser()
    cases = [
        (["status"], "status"),
        (["login"], "login"),
        (["login", "-u", "a@b.com"], "login"),
        (["login", "-u", "a@b.com", "-p", "secret"], "login"),
        (["login", "-f", "/tmp/x.tsv"], "login"),
        (["logout", "a@b.com", "c@d.com"], "logout"),
        (["--manager", "127.0.0.1:9999", "status"], "status"),
    ]
    for argv, expected in cases:
        ns = parser.parse_args(argv)
        assert ns.cmd == expected, (argv, ns)


# ---------------------------------------------------------------------------
# Driver
# ---------------------------------------------------------------------------


async def _run_async():
    cases = [
        ("success_no_2fa", t_success_no_2fa),
        ("success_with_2fa", t_success_with_2fa),
        ("failure_terminal", t_failure_terminal),
        ("unknown_intermediate_code_is_ignored", t_unknown_intermediate_code_is_ignored),
        ("stream_closes_without_terminal", t_stream_closes_without_terminal),
        ("empty_2fa_aborts", t_empty_2fa_aborts),
    ]
    failures = 0
    for name, fn in cases:
        try:
            await fn()
            print(f"PASS  async  {name}")
        except AssertionError as e:
            failures += 1
            print(f"FAIL  async  {name}: {e}")
    return failures


def main() -> int:
    failures = 0
    for name, fn in [("tsv_parser", t_tsv_parser), ("argparse_smoke", t_argparse_smoke)]:
        try:
            fn()
            print(f"PASS  sync   {name}")
        except AssertionError as e:
            failures += 1
            print(f"FAIL  sync   {name}: {e}")

    failures += asyncio.run(_run_async())

    if failures:
        print(f"\n{failures} test(s) FAILED")
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
