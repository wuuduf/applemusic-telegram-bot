# Apple Music Telegram Bot (Go)

面向 Telegram 场景的 Apple Music 下载机器人，支持 `song / album / playlist / station / music-video / artist`。

## 项目关系（继承链）

当前仓库：`wuuduf/applemusic-telegram-bot`（已在 GitHub 上挂到 fork network）

继承链：

1. `alacleaker/apple-music-alac-downloader`
2. `zhaarey/apple-music-downloader`
3. `moeleak/apple-music-downloader-bot`
4. `wuuduf/applemusic-telegram-bot`（本仓库）

本仓库在上游基础上继续演进，重点是 Telegram 运行时稳定性和工程化拆分。

## 核心能力

- 下载类型：`song` / `album` / `playlist` / `station` / `music-video`
- 扩展任务：封面、动态封面、歌词导出、艺人资源导出
- 发送策略：逐个发送 / ZIP 发送（超限自动回退）
- Telegram `file_id` 缓存复用（音频 / 视频 / ZIP）
- 队列 + Worker 并发（`worker1..worker4`）
- `retry_after` 全局背压、断点恢复、状态快照恢复
- 下载目录配额清理 + 资源守卫（磁盘 / tmp / 内存）

## 依赖

- Go `1.23.1+`
- [MP4Box](https://gpac.io/downloads/gpac-nightly-builds/)
- [wrapper](https://github.com/WorldObservationLog/wrapper)（必须运行）
- [mp4decrypt](https://www.bento4.com/downloads/)（MV 必需）
- `ffmpeg`（FLAC 转换、动态封面、可选转码）

## 快速开始

### 1) 配置

```bash
cp config.example.yaml config.yaml
```

至少设置：

- `telegram-bot-token`（或环境变量 `TELEGRAM_BOT_TOKEN`）
- `media-user-token`（歌词/AAC-LC/Station/MV 需要）

可选：

- `telegram-allowed-chat-ids`
- `storefront`（如 `us` / `jp`）

### 2) 启动 Bot

```bash
go run . --bot
```

### 3) CLI（非 Bot）

```bash
go run . https://music.apple.com/us/album/.../1234567890
go run . --song https://music.apple.com/us/song/.../1234567890
go run . --search song "Taylor Swift"
```

## 机器人命令

短命令优先：

- `/h` 帮助
- `/i` 查看 chat_id 或按资源 ID 下载
- `/sg <关键词>` 搜索歌曲
- `/sa <关键词>` 搜索专辑
- `/sr <关键词>` 搜索艺人
- `/s <type> <关键词>` 统一搜索（`song|album|artist`）
- `/u <apple-music-url>` 解析并下载
- `/rf <apple-music-url>` 强制重下
- `/ap <artist-url|artist-id>` 导出艺人资源
- `/cv <url|type id>` 仅封面
- `/ac <url|type id>` 仅动态封面
- `/ly <song|album target>` 导出歌词
- `/st [value]` 设置（格式/歌词/语言/songzip/worker 等）

补充 ID 命令：

- `/songid` `/albumid` `/playlistid` `/stationid` `/mvid` `/artistid`

## 关键配置项

- `telegram-cache-file`
- `telegram-state-file`
- `telegram-download-max-gb`
- `telegram-cleanup-interval-sec`
- `telegram-cleanup-scan-interval-sec`
- `telegram-cleanup-protect-sec`
- `telegram-resource-check-interval-sec`
- `telegram-min-free-disk-mb`
- `telegram-min-free-tmp-mb`
- `telegram-max-memory-mb`

完整示例见：[`config.example.yaml`](./config.example.yaml)

## Docker

```bash
docker build -t applemusic-telegram-bot .

docker run --rm -it \
  --network host \
  -v "$PWD/config.yaml":/app/config.yaml \
  -v "$PWD/downloads":/downloads \
  -v "$PWD/telegram-cache.json":/app/telegram-cache.json \
  -e TELEGRAM_BOT_TOKEN=your_bot_token \
  applemusic-telegram-bot --bot
```

> 如果 `wrapper` 不在容器内，请确保 `decrypt-m3u8-port` / `get-m3u8-port` 对容器可达。

## 自建 Telegram Bot API（可选）

已提供：

- `docker-compose.telegram-bot-api.yml`
- `.env.telegram-bot-api.example`

可在 `config.yaml` 中通过 `telegram-api-url` 指向自建地址。

## 项目结构

```text
/Users/jelly/github/applemusic-telegram-bot
├── cmd/applemusic-telegram-bot/
├── internal/
│   ├── app/
│   ├── catalog/
│   └── storage/
├── utils/
├── config.example.yaml
└── Dockerfile
```

## 开发

```bash
go test ./...
go build .
```

## 致谢

- [moeleak/apple-music-downloader-bot](https://github.com/moeleak/apple-music-downloader-bot)
- [zhaarey/apple-music-downloader](https://github.com/zhaarey/apple-music-downloader)
