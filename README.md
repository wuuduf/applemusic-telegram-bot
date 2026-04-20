# Apple Music Telegram Bot (Go)

面向 Telegram 场景的 Apple Music 下载机器人，支持 `song / album / playlist / station / music-video / artist / curator`。

## 项目关系（继承链）

当前仓库：`wuuduf/applemusic-telegram-bot`（已在 GitHub 上挂到 fork network）

继承链：

1. `alacleaker/apple-music-alac-downloader`
2. `zhaarey/apple-music-downloader`
3. `moeleak/apple-music-downloader-bot`
4. `wuuduf/applemusic-telegram-bot`（本仓库）

本仓库在上游基础上继续演进，重点是 Telegram 运行时稳定性和工程化拆分。

## 核心能力

- 下载类型：`song` / `album` / `playlist` / `station` / `music-video` / `curator(展开专辑列表)`
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
- `/st [value]` 设置（格式/歌词/语言/songzip/worker/歌曲赏析开关等）

补充 ID 命令：

- `/songid` `/albumid` `/playlistid` `/stationid` `/mvid` `/artistid`

## 关键配置项

- `telegram-cache-file`
- `lastfm-api-key`（用于歌曲赏析来源）
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

## Docker Compose 部署（推荐，从 git clone 开始）

### 1) 克隆项目

```bash
git clone https://github.com/wuuduf/applemusic-telegram-bot.git
cd applemusic-telegram-bot
```

### 2) 准备配置文件

```bash
cp config.example.yaml config.yaml
```

至少修改：

- `telegram-bot-token`（或用容器环境变量 `TELEGRAM_BOT_TOKEN`）
- `media-user-token`（歌词/AAC-LC/Station/MV 需要）

并把容器网络下的联动地址改成（很关键）：

```yaml
decrypt-m3u8-port: "wrapper:10020"
get-m3u8-port: "wrapper:20020"
telegram-api-url: "http://telegram-bot-api:8081"
telegram-cache-file: "/app/runtime/telegram-cache.json"
```

### 3) 按注释修改 `docker-compose.yml`

仓库根目录已提供四个服务（其中 `wrapper-init` 只在初始化时临时运行）：

- `wrapper-init`（一次性初始化/登录）
- `wrapper`（常驻）
- `telegram-bot-api`（本地 Bot API）
- `bot`（本项目）

请按文件内注释修改这些值：

- `wrapper-init` / `wrapper` 的 `image` 和 `platform`（按 `uname -m` 架构）
- `wrapper-init.environment.args`（你的 Apple ID 登录参数）
- `telegram-bot-api.environment.TELEGRAM_API_ID` / `TELEGRAM_API_HASH`
- `bot.environment.TELEGRAM_BOT_TOKEN`（可留空，改在 `config.yaml`）

### 4) 初始化宿主机目录与文件

```bash
mkdir -p rootfs/data data/telegram-bot-api downloads bot-runtime
touch bot-runtime/telegram-cache.json
```

> 注意：不要把缓存文件做单文件挂载（如 `./telegram-cache.json:/app/telegram-cache.json`）。  
> 程序使用 atomic write（临时文件 + rename），在 Docker 单文件 bind mount 下可能报 `device or resource busy`。

### 5) 执行一次 wrapper 初始化登录（按需）

```bash
docker compose --profile init run --rm wrapper-init
```

运行后请查看该容器日志输出；若提示需要 2FA，请在项目根目录另开一个终端写入验证码文件：

```bash
echo -n 240020 > rootfs/data/data/com.apple.android.music/files/2fa.txt
```

将 `240020` 替换为你 Apple 设备上当前显示的 6 位 2FA 验证码。

### 6) 启动全部服务

```bash
docker compose up -d --build
```

### 7) 查看运行状态

```bash
docker compose ps
docker compose logs -f bot
```

## Docker（单容器模式，可选）

```bash
docker build -t applemusic-telegram-bot .

docker run --rm -it \
  --network host \
  -v "$PWD/config.yaml":/app/config.yaml \
  -v "$PWD/downloads":/downloads \
  -v "$PWD/bot-runtime":/app/runtime \
  -e TELEGRAM_BOT_TOKEN=your_bot_token \
  applemusic-telegram-bot --bot
```

> 如果 `wrapper` 不在容器内，请确保 `decrypt-m3u8-port` / `get-m3u8-port` 对容器可达。

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
