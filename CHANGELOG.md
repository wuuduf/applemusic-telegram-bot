# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- SQLite persistence backend (default): the Telegram file_id cache, runtime state and download history now live in one database (`telegram.db` next to `telegram-cache-file`, override with `telegram-db-file`). Cache entries and history records are written individually instead of rewriting whole JSON files on every change. On first start, existing cache/state/history JSON files are imported and renamed to `*.migrated` as a backup; to roll back, stop the bot, rename them back and set `telegram-storage-backend: json`. Uses the pure-Go `modernc.org/sqlite` driver (no CGO; adds about 6 MB to the binary). The download cleanup never deletes the database, its `-wal`/`-shm` files, or the persistence/backup files.
- Per-user rate limiting / anti-abuse: token-bucket flood protection plus an optional rolling 24h quota for download and asset-export commands (and Apple Music URL messages). Admins are exempt. Configurable via `telegram-rate-limit-enabled`, `telegram-rate-limit-per-user-interval-sec`, `telegram-rate-limit-per-user-burst`, `telegram-rate-limit-per-user-daily-quota`.
- Per-chat download history with the new `/history [n]` command (also aliased `/hist`); recent outcomes are persisted atomically and surfaced in `/status`. Configurable via `telegram-history-enabled`, `telegram-history-per-chat-limit`, `telegram-history-file`.
- Transient-failure retries for external tools (mp4decrypt / MP4Box / ffmpeg): timeouts and process-spawn failures are retried with linear backoff, while non-zero exits (including death by signal), missing/non-executable binaries and stdin-fed commands are not. Configurable via `telegram-cmd-max-retries` / `telegram-cmd-retry-backoff-ms` (or `AMDL_CMD_MAX_RETRIES` / `AMDL_CMD_RETRY_BACKOFF_MS`).
- Optional bearer-token auth for the metrics/health HTTP endpoints via `telegram-metrics-auth-token`, with a startup warning when a non-loopback bind has no token.

### Security & stability
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
