# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
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
