# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- Optional `metadata-storefront` for Chinese-first song tags and lyrics independent of the audio storefront. Disabled by default; set `cn` (or another preferred two-letter storefront) to enable. Fallbacks are `cn` → `tw` → `hk` → `us`, with original-storefront lyrics retained when localized lyrics are unavailable. Matching uses catalog ID without conflicting ISRC, or exact ISRC plus duration (within 2s) for cross-ID results; ambiguous or incomplete/paginated search results are rejected. Lookups share an 8s per-track budget and leave filenames/audio routing untouched. Existing files/Telegram cache are not retagged automatically.
- `/quota` shows a user's download allowance: actions available now, time to the next refill, and 24h usage against the daily quota.
- Rate-limit refunds: when every task a message tried to queue is turned away because the queue is full or resources are under pressure, the charge for that message is returned. Messages that only open a selection panel or contain invalid input stay charged, so panels cannot be used to bypass the limiter.
- Command registry (`internal/app/command_registry.go`): one table now drives alias resolution, per-user rate limiting, the zh/en `/help` text and the optional Telegram command menu. Tests fail if a dispatched command is missing from the registry (or vice versa), or if a visible command is undocumented in the READMEs. `/help` output is unchanged.
- Optional `telegram-sync-command-menu` (default off): on startup, publish the `/help` command list as Telegram's "/" menu for the default (zh) and English locales via `setMyCommands`.
- SQLite persistence backend (default): the Telegram file_id cache, runtime state and download history now live in one database (`telegram.db` next to `telegram-cache-file`, override with `telegram-db-file`). Cache entries and history records are written individually instead of rewriting whole JSON files on every change. On first start, existing cache/state/history JSON files are imported and renamed to `*.migrated` as a backup; to roll back, stop the bot, rename them back and set `telegram-storage-backend: json`. Uses the pure-Go `modernc.org/sqlite` driver (no CGO; adds about 6 MB to the binary). The download cleanup never deletes the database, its `-wal`/`-shm` files, or the persistence/backup files.
- Per-user rate limiting / anti-abuse: token-bucket flood protection plus an optional rolling 24h quota for download and asset-export commands (and Apple Music URL messages). Admins are exempt. Configurable via `telegram-rate-limit-enabled`, `telegram-rate-limit-per-user-interval-sec`, `telegram-rate-limit-per-user-burst`, `telegram-rate-limit-per-user-daily-quota`.
- Per-chat download history with the new `/history [n]` command (also aliased `/hist`); recent outcomes are persisted atomically and surfaced in `/status`. Configurable via `telegram-history-enabled`, `telegram-history-per-chat-limit`, `telegram-history-file`.
- Transient-failure retries for external tools (mp4decrypt / MP4Box / ffmpeg): timeouts and process-spawn failures are retried with linear backoff, while non-zero exits (including death by signal), missing/non-executable binaries and stdin-fed commands are not. Configurable via `telegram-cmd-max-retries` / `telegram-cmd-retry-backoff-ms` (or `AMDL_CMD_MAX_RETRIES` / `AMDL_CMD_RETRY_BACKOFF_MS`).
- Optional bearer-token auth for the metrics/health HTTP endpoints via `telegram-metrics-auth-token`, with a startup warning when a non-loopback bind has no token.

### Security & stability
- The Docker workflow logs in to ghcr.io with the built-in `GITHUB_TOKEN` instead of the `GHCR_TOKEN` personal access token, which had expired and blocked every image push. The `GHCR_TOKEN` secret is no longer used.
- **Fix:** the download quota cleanup no longer treats an inherited `TMPDIR` as a bot-owned directory. On macOS (where `TMPDIR` is always the per-user temp folder) or with an explicitly exported `TMPDIR`, the cleanup could delete other programs' temporary files once the quota was exceeded. Only `AMDL_TMPDIR` and the bot's own `<download folder>/.tmp` are cleaned now; temporary files still go to an inherited `TMPDIR`.
- Fixed a data race when stopping the resource guard.
- End-to-end tests (`internal/app/e2e_test.go`) run a real bot runtime against a fake Telegram Bot API and a fake Apple Music API: link → download → upload → file_id cache reuse → `/history`; search selection charged by the rate limiter; only admins change the worker limit; `/cancel` of a running download; graceful restart resuming an interrupted and a queued download. Only the download step itself is stubbed.
- LRC timestamps are correct for TTML times with fewer than three fractional digits (`1:02.5` used to become `[01:02.00]`).
- A failed AAC decrypt no longer leaves a partial file that the next request would send as a finished download; a failed media download is reported instead of being decrypted as an empty file.
- Apple Music catalog and Last.fm requests use the general HTTP client instead of the Telegram one, so `telegram-proxy-url` / `telegram-no-proxy` no longer apply to them.
- The Widevine license request has a 45s timeout. Canceling a music-video download stops segment retries at once, and segments downloaded ahead of the writer are bounded (32) instead of accumulating in memory.
- Catalog listings (artist songs/albums, curator rooms) retry 429/5xx responses, stop on a `next` link that does not advance, and are capped at 500 pages.
- Album lyrics exports remove their temporary directory; artist/curator ZIP transfers are protected from quota cleanup like other ZIP transfers; `ffmpeg-path` is honored for animated covers and artist assets.
- wrapper-shim bounds each decrypt round trip to wrapper-manager (2 minutes) so a silent manager cannot pin a handler and its stream.
- `make login-one` no longer echoes the password (it is passed through the environment) and refuses to run when `USER=` was not given on the command line instead of using the OS login name.
- CI runs staticcheck (pinned to v0.8.2). Removed the unused code it reported (37 functions/types left behind by earlier refactors) and fixed the remaining findings; retries of Apple API network errors now key on timeouts only, since `net.Error.Temporary` is deprecated.
- Upgrade to Go 1.26.11 (go.mod toolchain, CI, both Dockerfiles) for GO-2026-6859 in `net/http`, which govulncheck reports against 1.26.10.
- A graceful stop or restart no longer forgets queued tasks: requests that had not started, and the one cut short by the shutdown, stay in the saved state and are re-queued on the next start (previously this only worked after a hard kill).
- The Apple web token is fetched on demand from the shared cache for every request, so lyrics and playback requests follow a refresh too instead of waiting for the next restart. A configured `authorization-token` is still used as given.
- Optional `telegram-cache-max-age-days` (default 0 = keep forever): drop file_id cache entries not written for that many days at startup.
- Experimental `telegram-download-concurrency` (default 1 = unchanged, strictly serial): let up to 4 downloads run in the download core at once. Downloads of the same item stay serialized; the wrapper must accept several decrypt connections, and a single song plus the album that contains it can still write the same file at the same time.
- Search results are now charged against the per-user limiter when one is picked. Search commands are free, so search + tap used to start unlimited downloads.
- **Behavior change:** the global worker limit (`/settings workerN` and the settings-panel buttons) can only be changed by admins (`telegram-admin-user-ids`); previously any allowed user could change it.
- The wrapper sockets are bounded: the decrypt connection has per-phase deadlines and follows task cancellation, and the device m3u8 lookup has a dial timeout and a 30s deadline. A stalled wrapper could previously block every download.
- The Apple web token is re-read at each bot runtime start (daily restart included), only unexpired tokens are accepted, and a 401 re-scrapes Apple only when the cached token itself was rejected.
- `/cancel` now also stops a ZIP upload and manifest fetches in progress.
- Quota cleanup no longer deletes files a running task still has to upload (ZIP/bundle transfers protect everything they download; one-by-one transfers pin the track being sent).
- A request that was running during two crashes is dropped on recovery (with a notice to the chat) instead of being re-queued forever.
- Tasks start in queue order even with several workers; runtime-state saves are debounced (300ms).
- **Behavior change:** the memory guard (`telegram-max-memory-mb`) now measures the container's anonymous memory (cgroup, includes ffmpeg/MP4Box child processes) or the process RSS instead of the Go heap, so existing thresholds may need raising.
- Media captions are signed with the running bot's own username (from `getMe`) instead of a hard-coded one; override or disable with `telegram-caption-signature`.
- Fixed leaks: upload watchdog contexts, a temp file when FLAC compression cannot reach the size limit, and repeated FLAC compression on upload retries. Inline-query and `/id <song>` IDs are validated before use.
- CI verifies `go.mod`/`go.sum` are tidy for the root module too.
- Upgrade to Go 1.26.10 (go.mod `go 1.26.0`, CI, both Dockerfiles). Go 1.25 is no longer supported and govulncheck reported 10 standard-library vulnerabilities (net/http, crypto/x509, crypto/tls, net/textproto) that are fixed only in Go 1.26.9+. Also bump `golang.org/x/net` to v0.60.0 in both modules and wrapper-shim's `google.golang.org/grpc` to v1.83.1 (GO-2026-6348, GO-2026-6061). Both modules now scan clean, and CI runs govulncheck for wrapper-shim as well.
- CI actions upgraded to their Node 24 releases (checkout v7, setup-go v7, upload-artifact v7, docker login/setup-buildx v4, build-push v7, golangci-lint-action v9, action-gh-release v3), removing the Node 20 deprecation warnings before GitHub drops Node 20. None of the removed inputs were in use.
- Docker CI now always builds the image (and the wrapper-shim image) and only skips the push when logging in to ghcr.io fails, failing with a clear message; pull requests get a build-only check. Previously an expired `GHCR_TOKEN` failed the job before any build, hiding whether the Dockerfile still worked.
- `/dg` (request a song), `/motioncover` and `/lyric` now go through the per-user rate limiter like the other download commands; previously they bypassed it.
- Download directories are now created with `0o755` instead of world-writable `0o777`.
- wrapper-shim decrypt/m3u8 per-connection handlers now recover from panics so a malformed frame can no longer crash the shim; the shim's TCP listeners now default to loopback (container deployments still pass explicit bind flags).
- Metrics HTTP server gained a `WriteTimeout`.
- Log redaction now also masks Apple `media-user-token` / `authorization-token` as defense-in-depth, in addition to the Telegram bot token.
- Docker image now builds with Go 1.25.13 (matching CI) to pick up the stdlib fixes govulncheck flagged.
- wrapper-shim now checks in `go.sum`; its Docker build uses `go mod download && go mod verify` instead of resolving dependencies with `go mod tidy` at build time, builds with Go 1.25.13, and CI vets, tests and builds it (including a tidy check).
- Apple Music catalog requests no longer fall back to `http.DefaultClient` (no timeout); they use the shared timeout-bound client.
- Search selection metadata now expires after 24h and is capped at 5000 entries instead of growing forever in memory and in the state file.
- Enqueuing a download during shutdown can no longer panic with "send on closed channel"; the queue is closed under the same lock used by senders.
- External command output capture is bounded to the last 256 KiB per stream, so a chatty ffmpeg run cannot balloon memory; error messages still show the tail where the real error is.
- The runtime Docker image no longer ships `curl` and `unzip`; the Bento4 `mp4decrypt` download now happens in a separate build stage.

### Performance
- Cache saves snapshot under the lock and marshal/write outside it, so lookups no longer stall during disk I/O; cache and state files are written compactly, and stale snapshots can no longer overwrite newer ones. The state file now uses the same fsync'd, symlink-safe atomic writer as the cache.
- Telegram updates are now handled concurrently across chats (updates from the same chat stay in order), so a slow search or Apple Music API call in one chat no longer stalls every other user. Configurable via `telegram-update-concurrency` (default 8, `1` restores fully serial handling).
- ZIP transfers store already-compressed media (m4a/flac/mp4/jpg/…) instead of deflating it, cutting CPU time for album/playlist ZIPs at virtually no size cost.

- Telegram bot support for Apple Music URL types: song, album, playlist, artist, station, music-video.
- New bot commands: `/playlistid`, `/stationid`, `/mvid`, `/artistid`, `/url`, expanded `/id`.
- New Telegram command: `/chatid` (also `/sessionid`) to show current `chat_id` for whitelist setup.
- Artist secondary flow for `Albums` vs `Music Videos`.
- Unified transfer mode for album/playlist/station: one-by-one and ZIP.
- MV download/send flow with cache-aware re-send.
- Telegram settings support for `alac`, `flac`, `aac`, `atmos`, `aac-type`, `mv-audio-type`.
- `config.example.yaml` and open-source governance files.
- Telegram settings panel now includes an `Exit and delete` button to close the panel quickly.
- Telegram interactive panels now include a unified `Cancel and delete` button (search, paging, transfer mode, artist sub-selection, settings).
- `/artistphoto` (`/ap`) now supports exporting artist profile image plus all album covers and animated covers with one-by-one/ZIP selection.
- Request-scoped download context types (`DownloadSession` / `JobContext`) to isolate mutable task state across CLI, Telegram, and AstrBot `/v1/download`.
- Safe access helper package (`utils/safe`) with typed `AccessError` to avoid panic-prone direct indexing/slicing for API response fields.
- Unified external command runner (`utils/cmdrunner`) with context-aware execution, default timeout, kill-on-timeout, and structured output-rich errors.
- AstrBot artifact janitor with periodic cleanup by max age + max size quota and write-protection window for in-flight artifacts.
- Telegram cleanup tracker with incremental accounting and periodic fallback scan to avoid per-task full directory scanning.
- New tests for safe access, external command runner, zip creation, AstrBot artifact cleanup, and Telegram cleanup tracker behavior.

### Changed
- **Log format change:** bot-runtime logging goes through a `log/slog` logger. Console lines are now `<time> <LEVEL> <message>`; the level defaults to `info` and can be set with `AMDL_LOG_LEVEL` (`debug`/`info`/`warn`/`error`). `ERROR` records are also appended to the runtime error log file, whose line format is unchanged. The bot token, Apple Music tokens and the Last.fm `api_key` are redacted centrally in the log handler and in the error log file. The interactive CLI output and per-track download progress still print as before.
- Bot API calls share one helper (`callTelegramJSON`) that returns a structured error (HTTP status, `error_code`, description, `retry_after`); rate-limit and retry decisions read those fields and parse error text only for errors without structure. Error messages are unchanged. `answerCallbackQuery`, `answerInlineQuery` and `deleteWebhook` now stop with the bot's shutdown context.
- Album/playlist/station/song workspace setup shares its folder-naming and cover helpers; resulting paths are unchanged.
- Telegram cache now supports song audio + MV + ZIP bundle `file_id`.
- Help text and README docs updated to match real bot behavior.
- CI artifacts and Docker config now use safe default config template.
- Bot polling errors now redact Telegram token in console logs.
- Telegram song download transfer now follows per-chat setting (`songzip`) instead of prompting transfer selection.
- Telegram task worker count is now configurable in settings (`worker1` ... `worker4`, default `worker1`).
- AstrBot API download now allows `transfer_mode=zip` for `song` targets.
- Unauthorized reply now includes `chat_id` hint to help configure `telegram-allowed-chat-ids`.
- Telegram auto-cleanup now also considers `AMDL_TMPDIR`/`TMPDIR` when set to dedicated paths (shared `/tmp` and `/var/tmp` are skipped).
- Telegram help text is now Chinese.
- Telegram `/help` now prioritizes short commands (`/h`, `/i`, `/sg`...) while keeping legacy commands compatible.
- `chat_id` is no longer auto-shown in `/start`/`/help` or unauthorized replies.
- `/id` (without args) now shows current `chat_id`; `/id <...>` behavior remains for media downloads.
- Telegram auto extras (`lyrics` / `cover` / `animated`) are now disabled by default for new chat settings.
- Apple API/downloader outbound requests now use a shared HTTP client with configurable timeout (`AMDL_HTTP_TIMEOUT_SEC`, default `45s`).
- `runv2` now uses configurable stream idle timeout (`AMDL_RUNV2_IDLE_TIMEOUT_SEC`, default `300s`) instead of unlimited waits.
- Telegram `song` downloads now always embed lyrics + cover into the audio file; auto extras (`lyrics/cover/animated`) now only affect separate attachment sending.
- CLI artist expansion now updates session/template-derived config instead of mutating global `Config.ArtistFolderFormat`.
- `writeMP4Tags` now consumes request/session config for playlist metadata behavior (`UseSongInfoForPlaylist`) instead of reading global mutable config.
- Album/playlist/station/song/MV metadata parsing paths now route through safe access helpers and return typed errors for missing/invalid fields.
- `ffmpeg`, `MP4Box`, `mp4decrypt`, and `ffprobe` execution paths now use the centralized command runner with consistent timeout/cancellation/error reporting.
- AstrBot artifact cleanup switched from request-triggered cleanup to startup + periodic janitor mode.
- Telegram download cleanup switched to tracker + timer driven cleanup, with periodic rescan fallback.
- Added config/env controls for AstrBot artifact retention/quota/janitor interval and Telegram cleanup interval/scan interval/protect window.

### Fixed
- `go vet` protobuf lock-copy warning in `utils/runv3/cdm/cdm.go`.
- MV segmented downloader now retries transient segment failures and reports incomplete segment errors clearly instead of producing partial outputs.
- Avoided connection/body handle buildup in paginated Apple API loops by closing response bodies per iteration.
- Improved request failure handling for token/lyrics/webPlayback/download stream paths (clear status checks and decode errors).
- Removed panic-prone direct access patterns such as `Data[0]`, `GenreNames[0]`, `ReleaseDate[:4]`, and relationship `Data[0]` in key API/metadata paths.
- External command failures now preserve actionable stderr/combined output context, including identifiable timeout/cancel failure reasons.
