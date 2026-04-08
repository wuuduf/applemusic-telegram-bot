# Apple Music Telegram Bot（Go）

一个**独立维护**的 Apple Music 下载机器人项目：
- 保留并增强 Telegram Bot 场景
- 保留 CLI 下载入口（非 Bot 模式）
- 在结构上做了较大重构（从单文件演进到模块化）

---

## 项目关系与继承（重要）

你提到的两个参考仓库关系是准确的，本仓库在此基础上继续独立演进。

### 继承链路

1. `zhaarey/apple-music-downloader`（上游下载器主干）
2. `moeleak/apple-music-downloader-bot`（在上游基础上加入 Telegram Bot 能力）
3. `wuuduf/applemusic-telegram-bot`（本仓库，独立项目，继续重构和增强）

### 基于 GitHub 元数据（抓取时间：2026-04-08）

- `zhaarey/apple-music-downloader`：`fork=true`，parent 为 `alacleaker/apple-music-alac-downloader`
- `moeleak/apple-music-downloader-bot`：`fork=true`，parent 为 `zhaarey/apple-music-downloader`
- `wuuduf/applemusic-telegram-bot`：`fork=false`，创建于 **2026-04-08**（独立仓库）

### 本仓库相对参考项目的主要变化

- 从单体 `main.go` 拆分为 `internal/` + `utils/` 模块
- Telegram 命令与任务类型更完整（song/album/playlist/station/mv/artist）
- 增加运行时可靠性能力：
  - 队列 + worker（`worker1..worker4`）
  - `retry_after` 背压
  - `file_id` 缓存复用（音频/视频/ZIP）
  - 状态快照恢复（重启后恢复队列/面板状态）
  - 下载目录配额清理 + 资源守护（磁盘/临时目录/内存）
- 新增并完善测试（当前仓库已有多组 `*_test.go`）

---

## 功能概览

- 支持资源类型：
  - `song`
  - `album`
  - `playlist`
  - `station`
  - `music-video`
  - `artist`（封面/资源导出场景）
- 发送模式：逐个发送 / ZIP 发送（超限自动回退）
- 每会话独立设置：
  - 音质（ALAC/FLAC/AAC/ATMOS）
  - AAC 类型 / MV 音轨类型
  - 歌词格式（LRC/TTML）
  - 语言（中文/English）
  - song ZIP 开关
  - worker 数量
- 额外任务：封面、动态封面、歌词导出、艺人资源导出

---

## 依赖要求

- Go **1.23.1+**
- [MP4Box](https://gpac.io/downloads/gpac-nightly-builds/)
- [wrapper](https://github.com/WorldObservationLog/wrapper)（必须运行）
- [mp4decrypt](https://www.bento4.com/downloads/)（MV 必需）
- `ffmpeg`（FLAC 转换 / 动态封面 / 可选转码）

---

## 快速开始（Telegram Bot）

### 1）准备配置

```bash
cp config.example.yaml config.yaml
```

至少改这些项：

- `telegram-bot-token`（或环境变量 `TELEGRAM_BOT_TOKEN`）
- `media-user-token`（需要歌词/AAC-LC/电台/MV 时必填）

可选：

- `telegram-allowed-chat-ids`（白名单）
- `storefront`（如 `us` / `jp`）

### 2）启动

```bash
go run . --bot
```

---

## CLI 用法（非 Bot 模式）

仍然支持直接 CLI 下载，例如：

```bash
# 专辑
go run . https://music.apple.com/us/album/.../1234567890

# 单曲
go run . --song https://music.apple.com/us/song/.../1234567890

# 交互搜索
go run . --search song "Taylor Swift"
```

常见参数：`--song` `--select` `--atmos` `--aac` `--search`。

---

## 机器人命令（短命令优先）

| 短命令 | 长命令 | 说明 |
|---|---|---|
| `/h` | `/help` | 帮助 |
| `/i` | `/id` | 查看 chat_id 或按资源 ID 下载 |
| `/sg` | `/search_song` | 搜索歌曲 |
| `/sa` | `/search_album` | 搜索专辑 |
| `/sr` | `/search_artist` | 搜索艺人 |
| `/s` | `/search` | 统一搜索（song/album/artist） |
| `/u` | `/url` | 解析 Apple Music 链接并下载 |
| `/rf` | `/refresh` | 强制重下 |
| `/ap` | `/artistphoto` | 导出艺人图片/专辑封面/动态封面 |
| `/cv` | `/cover` | 仅封面 |
| `/ac` | `/animatedcover` | 仅动态封面 |
| `/ly` | `/lyrics` | 歌词导出 |
| `/st` | `/settings` | 下载设置（格式/歌词/语言/worker 等） |

补充 ID 命令：`/songid` `/albumid` `/playlistid` `/stationid` `/mvid` `/artistid`。

---

## 关键配置项（建议先看）

- `telegram-cache-file`：Telegram `file_id` 缓存文件
- `telegram-state-file`：运行时状态快照文件（重启恢复）
- `telegram-download-max-gb`：下载目录配额上限
- `telegram-cleanup-interval-sec`：周期清理间隔
- `telegram-resource-check-interval-sec`：资源守护检查间隔
- `telegram-min-free-disk-mb` / `telegram-min-free-tmp-mb` / `telegram-max-memory-mb`：资源阈值

完整项请直接看：[`config.example.yaml`](./config.example.yaml)

---

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

---

## 自建 Telegram Bot API（可选）

仓库提供：

- `docker-compose.telegram-bot-api.yml`
- `.env.telegram-bot-api.example`

可用于本地自建 Telegram Bot API 服务，然后在 `config.yaml` 中设置 `telegram-api-url`。

---

## 项目结构

```text
/Users/jelly/github/applemusic-telegram-bot
├── cmd/applemusic-telegram-bot/
├── internal/
│   ├── app/        # Bot 运行时、任务编排、状态恢复、上传与清理
│   ├── catalog/    # 封面/歌词/艺人关系等统一服务层
│   └── storage/    # 存储边界与清理根路径策略
├── utils/          # ampapi/runv2/runv3/lyrics/task 等底层模块
├── config.example.yaml
└── Dockerfile
```

---

## 开发

```bash
go test ./...
go build .
```

---

## 安全与合规提醒

- 不要提交真实 token 到仓库
- 优先使用 `https://` 的 `telegram-api-url`
- 本项目仅用于技术研究与学习，请遵守当地法律与平台条款

---

## 致谢

- 参考与继承：
  - [moeleak/apple-music-downloader-bot](https://github.com/moeleak/apple-music-downloader-bot)
  - [zhaarey/apple-music-downloader](https://github.com/zhaarey/apple-music-downloader)
- 依赖生态与工具作者（wrapper / mp4ff / go-mp4tag / ffmpeg / MP4Box 等）
