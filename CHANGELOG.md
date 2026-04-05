# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- Telegram bot support for Apple Music URL types: song, album, playlist, artist, station, music-video.
- New bot commands: `/playlistid`, `/stationid`, `/mvid`, `/artistid`, `/url`, expanded `/id`.
- Artist secondary flow for `Albums` vs `Music Videos`.
- Unified transfer mode for album/playlist/station: one-by-one and ZIP.
- MV download/send flow with cache-aware re-send.
- Telegram settings support for `alac`, `flac`, `aac`, `atmos`, `aac-type`, `mv-audio-type`.
- `config.example.yaml` and open-source governance files.

### Changed
- Telegram cache now supports song audio + MV + ZIP bundle `file_id`.
- Help text and README docs updated to match real bot behavior.
- CI artifacts and Docker config now use safe default config template.
- Bot polling errors now redact Telegram token in console logs.

### Fixed
- `go vet` protobuf lock-copy warning in `utils/runv3/cdm/cdm.go`.

