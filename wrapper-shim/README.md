# wrapper-shim

`wrapper-shim` is a tiny standalone proxy that lets clients of the original
[`WorldObservationLog/wrapper`](https://github.com/WorldObservationLog/wrapper)
raw-TCP protocol talk to a multi-account
[`WorldObservationLog/wrapper-manager`](https://github.com/WorldObservationLog/wrapper-manager)
gRPC backend without changing a single line of client code.

It exists so that this bot (and any other tool wired to the original wrapper's
two TCP ports) can be migrated to wrapper-manager just by pointing the same
config values at the shim.

## How it works

The bot speaks two raw-TCP protocols against the original wrapper:

| Endpoint           | Bot config key       | Default port | Purpose                          |
| ------------------ | -------------------- | -----------: | -------------------------------- |
| Get enhanced m3u8  | `get-m3u8-port`      |        20020 | `[1B len][adamId]` -> `<url>\n`  |
| Decrypt sample(s)  | `decrypt-m3u8-port`  |        10020 | length-prefixed sample stream    |

`wrapper-manager` exposes the same operations but over a single gRPC port
(`:8080` by default). The shim listens on the bot's TCP ports and translates
each request into the matching gRPC call:

```
bot ──TCP─▶ wrapper-shim ──gRPC─▶ wrapper-manager ──TCP─▶ wrapper instance(s)
```

The wire format the shim presents to the bot is byte-for-byte identical to
the original wrapper, including:

* The initial `[1B len][adamId][1B len][keyURI]` state setup on the decrypt
  port.
* Per-sample `[uint32 LE len][bytes]` send / `len` bytes recv frames.
* The `00 00 00 00` "switch keys" marker followed by a fresh state.
* The `00 00 00 00 00` close marker.
* The `"0"` placeholder adamId convention used together with the Apple
  prefetch key. The shim still routes the gRPC request under the most
  recent real adamId so wrapper-manager's region dispatcher pins the right
  instance for the song.

See [`internal/shim/protocol_test.go`](internal/shim/protocol_test.go) for
end-to-end protocol assertions against an in-process fake manager.

## Build

```sh
go build -o wrapper-shim ./cmd/wrapper-shim
```

A multi-arch container image is also provided:

```sh
docker build -t wrapper-shim:local .
```

## Run

```sh
wrapper-shim \
  -manager 127.0.0.1:8080 \
  -m3u8-listen :20020 \
  -decrypt-listen :10020 \
  -wait-ready 60s
```

Flags:

| Flag                | Default              | Notes                                                                       |
| ------------------- | -------------------- | --------------------------------------------------------------------------- |
| `-manager`          | `127.0.0.1:8080`     | Address of the wrapper-manager gRPC server.                                 |
| `-m3u8-listen`      | `:20020`             | TCP listen address for the get-m3u8 shim.                                   |
| `-decrypt-listen`   | `:10020`             | TCP listen address for the decrypt shim.                                    |
| `-wait-ready`       | `1m`                 | How long to poll `Status` for `Ready=true` on startup. Set to `0` to skip.  |

## Migrating the bot from `wrapper` to `wrapper-manager`

1. Bring up `wrapper-manager` and log in at least one Apple ID via its
   gRPC `Login` stream (use the manager's own tooling for this; the shim
   is data-plane only and intentionally does not expose `Login`).
2. Run `wrapper-shim` next to `wrapper-manager`, exposing the same two
   TCP ports the original `wrapper` used (`:10020` and `:20020`).
3. Leave the bot's `decrypt-m3u8-port` and `get-m3u8-port` config values
   pointing at those two ports. **No bot code or config schema changes
   are required.**

A reference Compose layout is provided in the parent project's
`docker-compose.yml` under the `wrapper-manager` profile.

## Limitations

* The shim only proxies `M3U8` and `Decrypt`. `Login`, `Logout`, `Lyrics`,
  `License`, `WebPlayback`, and `Status` are intentionally not exposed on
  TCP — those are operator-facing flows and clients that need them should
  speak gRPC directly to wrapper-manager.
* No authentication or TLS. The shim must run on a trusted network segment
  (typically the same Docker network as wrapper-manager).
* The shim opens one bidi `Decrypt` gRPC stream per accepted TCP connection
  and serializes samples within that stream. It does not reorder or
  multiplex samples across streams.
