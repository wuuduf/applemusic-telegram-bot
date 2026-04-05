English / [简体中文](./README-CN.md)

### ！！Must be installed first [MP4Box](https://gpac.io/downloads/gpac-nightly-builds/)，And confirm [MP4Box](https://gpac.io/downloads/gpac-nightly-builds/) Correctly added to environment variables

### Add features

1. Supports inline covers and LRC lyrics（Demand`media-user-token`，See the instructions at the end for how to get it）
2. Added support for getting word-by-word and out-of-sync lyrics
3. Support downloading singers `go run main.go https://music.apple.com/us/artist/taylor-swift/159260351` `--all-album` Automatically select all albums of the artist
4. The download decryption part is replaced with Sendy McSenderson to decrypt while downloading, and solve the lack of memory when decrypting large files
5. MV Download, installation required[mp4decrypt](https://www.bento4.com/downloads/)
6. Add interactive search with arrow-key navigation `go run main.go --search [song/album/artist] "search_term"`

### Special thanks to `chocomint` for creating `agent-arm64.js`

For acquisition`aac-lc` `MV` `lyrics` You must fill in the information with a subscription`media-user-token`

- `alac (audio-alac-stereo)`
- `ec3 (audio-atmos / audio-ec3)`
- `aac (audio-stereo)`
- `aac-lc (audio-stereo)`
- `aac-binaural (audio-stereo-binaural)`
- `aac-downmix (audio-stereo-downmix)`
- `MV`

# Apple Music ALAC / Dolby Atmos Downloader

Original script by Sorrow. Modified by me to include some fixes and improvements.

## Running with Docker

1. Make sure the decryption program [wrapper](https://github.com/WorldObservationLog/wrapper) is running

2. Start the downloader with Docker:
   ```bash
   # show help
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --help

   # start downloading some albums
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader https://music.apple.com/ru/album/children-of-forever/1443732441 

   # start downloading single song
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --song https://music.apple.com/ru/album/bass-folk-song/1443732441?i=1443732453

   # start downloading select
   docker run -it --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --select https://music.apple.com/ru/album/children-of-forever/1443732441

   # start downloading some playlists
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader https://music.apple.com/us/playlist/taylor-swift-essentials/pl.3950454ced8c45a3b0cc693c2a7db97b

   # for dolby atmos
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --atmos https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538
   
   # for aac
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --aac https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538

   # for see quality
   docker run --network host -v ./downloads:/downloads ghcr.io/zhaarey/apple-music-downloader --debug https://music.apple.com/ru/album/miles-smiles/209407331
   ```

You can change `config.yaml` by mounting a volume:

> **Note:** Before running the following command, create a local config first:
> `cp config.example.yaml config.yaml`
> If `./config.yaml` does not exist, Docker will create an empty directory instead of a file, which will cause the container to fail.
```bash
docker run --network host -v ./downloads:/downloads -v ./config.yaml:/app/config.yaml ghcr.io/zhaarey/apple-music-downloader [args]
```

## How to use
1. Make sure the decryption program [wrapper](https://github.com/WorldObservationLog/wrapper) is running
2. Start downloading some albums: `go run main.go https://music.apple.com/us/album/whenever-you-need-somebody-2022-remaster/1624945511`.
3. Start downloading single song: `go run main.go --song https://music.apple.com/us/album/never-gonna-give-you-up-2022-remaster/1624945511?i=1624945512` or `go run main.go https://music.apple.com/us/song/you-move-me-2022-remaster/1624945520`.
4. Start downloading select: `go run main.go --select https://music.apple.com/us/album/whenever-you-need-somebody-2022-remaster/1624945511` input numbers separated by spaces.
5. Start downloading some playlists: `go run main.go https://music.apple.com/us/playlist/taylor-swift-essentials/pl.3950454ced8c45a3b0cc693c2a7db97b` or `go run main.go https://music.apple.com/us/playlist/hi-res-lossless-24-bit-192khz/pl.u-MDAWvpjt38370N`.
6. For dolby atmos: `go run main.go --atmos https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538`.
7. For aac: `go run main.go --aac https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538`.
8. For see quality: `go run main.go --debug https://music.apple.com/us/album/1989-taylors-version-deluxe/1713845538`.

[Chinese tutorial - see Method 3 for details](https://telegra.ph/Apple-Music-Alac高解析度无损音乐下载教程-04-02-2)

## Docker
Build the image:
```
docker build -t apple-music-dl .
```

Run the bot:
```
docker run --rm -it \
  -v "$PWD/config.yaml":/app/config.yaml \
  -v "$PWD/downloads":/downloads \
  -v "$PWD/telegram-cache.json":/app/telegram-cache.json \
  -e TELEGRAM_BOT_TOKEN=your_bot_token \
  apple-music-dl --bot
```

Notes:
- Mount `telegram-cache.json` only if you enable `telegram-cache-file`.
- The bot uses long polling; no port mapping is required.
- Never commit real values in `config.yaml`. Keep tracked defaults in `config.example.yaml`.

## Telegram bot mode
1. Copy `config.example.yaml` to `config.yaml`, then set `telegram-bot-token` (or export `TELEGRAM_BOT_TOKEN`).
2. Optional: set `telegram-allowed-chat-ids` to restrict usage.
3. Optional: set `telegram-api-url` to override the Telegram API base URL (`https://` is recommended; `http://` will print a security warning).
4. Start the bot: `go run main.go --bot`
5. Commands:
   - `/search_song <keywords>`
   - `/search_album <keywords>`
   - `/search_artist <keywords>`
   - `/search <type> <keywords>` (`type`: `song|album|artist`)
   - `/songid <id>`
   - `/albumid <id>`
   - `/playlistid <id>`
   - `/stationid <id>`
   - `/mvid <id>`
   - `/artistid <id>` (choose Albums or Music Videos)
   - `/id <song|album|playlist|station|mv|artist> <id>`
   - `/url <apple-music-url>`
   - `/settings [alac|flac|aac|atmos|aac-lc|aac-binaural|aac-downmix|ac3]`

6. You can also send Apple Music URLs directly in chat. The bot auto-detects:
   - `song`
   - `album`
   - `playlist`
   - `artist`
   - `station`
   - `music-video`

Notes:
- Default format is ALAC. `/settings` now supports ALAC/FLAC/AAC/Atmos plus AAC profile and MV audio profile.
- Artist flow supports a second step: choose `Albums` or `Music Videos`.
- Album/Playlist/Station transfer mode is unified: `one by one` or `ZIP`.
- ZIP results are cached via Telegram `file_id` for album/playlist/station.
- MV supports send-as-video, fallback-to-document, and Telegram `file_id` cache re-send.
- If ZIP is too large for Telegram, the bot falls back to one-by-one transfer automatically.
- If the download folder exceeds the limit, older files are removed (default 3GB; set `telegram-download-max-gb`, Telegram cache remains).
- Large files are re-encoded to fit `telegram-max-file-mb` in FLAC mode (quality may be reduced).
- For localized search results, set `telegram-search-language` (e.g. `zh-Hans`) or the global `language`.
- To enable instant re-sends, set `telegram-cache-file` so the bot can reuse Telegram file IDs (song audio + MV + ZIP).
- Share buttons require enabling inline mode in BotFather.

## Downloading lyrics

1. Open [Apple Music](https://music.apple.com) and log in
2. Open the Developer tools, Click `Application -> Storage -> Cookies -> https://music.apple.com`
3. Find the cookie named `media-user-token` and copy its value
4. Paste the cookie value obtained in step 3 into the setting called "media-user-token" in config.yaml and save it
5. Start the script as usual

## Get translation and pronunciation lyrics (Beta)

1. Open [Apple Music](https://beta.music.apple.com) and log in.
2. Open the Developer tools, click `Network` tab.
3. Search a song which is available for translation and pronunciation lyrics (recommend K-Pop songs).
4. Press Ctrl+R and let Developer tools sniff network data.
5. Play a song and then click lyric button, sniff will show a data called `syllable-lyrics`.
6. Stop sniff (small red circles button on top left), then click `Fetch/XHR` tabs.
7. Click `syllable-lyrics` data, see requested URL.
8. Find this line `.../syllable-lyrics?l=<copy all the language value from here>&extend=ttmlLocalizations`.
9. Paste the language value obtained in step 8 into the config.yaml and save it.
10. If don't need pronunciation, do this `...%5D=<remove this value>&extend...` on config.yaml and save it.
11. Start the script as usual.

Noted: These features are only in beta version right now.
