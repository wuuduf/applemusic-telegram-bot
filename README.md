English / [简体中文](./README-CN.md)

# Apple Music Telegram Bot (Go)

This repository is the **Telegram Bot only** extraction of the Apple Music downloader runtime.
AstrBot API mode has been removed.

## Features

- Telegram bot download flow for:
  - `song`
  - `album`
  - `playlist`
  - `station`
  - `music-video`
- One-by-one / ZIP transfer flow
- Telegram `file_id` cache reuse (song audio + MV + ZIP)
- In-flight dedup, queue + workers, retry/backpressure handling
- Runtime state snapshot restore and background cleanup janitor
- Bot `/settings` options for format/lyrics/cover/worker controls

## Requirements

- [MP4Box](https://gpac.io/downloads/gpac-nightly-builds/)
- [wrapper](https://github.com/WorldObservationLog/wrapper) running
- [mp4decrypt](https://www.bento4.com/downloads/) (required for MV)
- `ffmpeg` (required for animated cover and optional conversion)

## Quick Start

1. Copy config template:

   ```bash
   cp config.example.yaml config.yaml
   ```

2. Set bot token (in `config.yaml` or env):

   - `telegram-bot-token: "..."`
   - or `TELEGRAM_BOT_TOKEN=...`

3. (Optional) Restrict chat access:

   - `telegram-allowed-chat-ids: [123456789]`

4. Start bot:

   ```bash
   go run . --bot
   ```

## Docker

Build image:

```bash
docker build -t applemusic-telegram-bot .
```

Run bot:

```bash
docker run --rm -it \
  -v "$PWD/config.yaml":/app/config.yaml \
  -v "$PWD/downloads":/downloads \
  -v "$PWD/telegram-cache.json":/app/telegram-cache.json \
  -e TELEGRAM_BOT_TOKEN=your_bot_token \
  applemusic-telegram-bot --bot
```

## Bot Commands

- `/h` help
- `/i` show chat_id or download by media id
- `/sg <keywords>` search songs
- `/sa <keywords>` search albums
- `/sr <keywords>` search artists
- `/s <type> <keywords>` unified search (`song|album|artist`)
- `/u <apple-music-url>` parse + download
- `/rf <apple-music-url>` force refresh
- `/ap <artist-url|artist-id>` artist assets export
- `/cv <url|type id>` cover only
- `/ac <url|type id>` animated cover only
- `/ly <song/album target>` lyrics export
- `/st [alac|flac|aac|atmos|...|songzip|worker1..worker4]`

Legacy aliases are still supported.

## Important Telegram Runtime Notes

- `song` mode supports one-by-one/zip behavior via settings.
- ZIP too large => auto fallback to one-by-one.
- `retry_after` responses trigger global upload backpressure.
- Runtime state auto-saved to `telegram-state-file` and recovered on restart.
- Background janitor is controlled by:
  - `telegram-download-max-gb`
  - `telegram-cleanup-interval-sec`
  - `telegram-cleanup-scan-interval-sec`
  - `telegram-cleanup-protect-sec`

## Security Notes

- Do not commit real tokens in `config.yaml`.
- Prefer `https://` for `telegram-api-url`.
- If self-hosting Telegram Bot API with plain HTTP, use only trusted networks.
