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
- 管理员面板、用户白名单 / 黑名单、管理员命令
- 歌曲 caption 增强（歌手 / 专辑 / 风格）
- 歌曲实时归档到指定群、缓存音频批量转存到指定群
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
- `telegram-admin-user-ids`
- `telegram-forward-chat-id`
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
- `/amwhoami` 查看当前 `user_id / chat_id`

补充 ID 命令：

- `/songid` `/albumid` `/playlistid` `/stationid` `/mvid` `/artistid`

批量能力说明：

- 一条消息中可直接发送**多个** Apple Music 链接，Bot 会按顺序逐个识别并排队下载
- `/u` 支持一次传入多个链接
- `/songid` `/albumid` `/playlistid` `/stationid` `/mvid` `/artistid` 支持一次传入多个 ID
- `/id <type> <id...>` 支持同类型批量，例如 `song | album | playlist | station | mv | artist | curator`
- 多 ID 分隔符支持：空格、英文逗号 `,`、中文逗号 `，`、英文分号 `;`、中文分号 `；`

示例：

```text
/songid 123 456 789
/albumid 111,222,333
/id curator 1702073195

https://music.apple.com/us/album/.../123
https://music.apple.com/us/playlist/.../pl.xxxxx
https://music.apple.com/us/curator/100-best-albums/1702073195
```

管理员命令：

- `/amadmin` 打开管理员面板
- `/amwlon` / `/amwloff` 开启/关闭用户白名单模式
- `/amwladd <user_id>` 添加白名单用户
- `/amwldel <user_id>` 移除白名单用户
- `/amban <user_id>` 封禁用户
- `/amunban <user_id>` 解除封禁
- `/amcachepush` 按顺序把 `telegram-cache.json` 里的音频缓存转存到归档群

管理员命令还支持**回复某个用户消息后直接执行**：

- 回复用户消息发送 `/amwladd`
- 回复用户消息发送 `/amwldel`
- 回复用户消息发送 `/amban`
- 回复用户消息发送 `/amunban`

权限规则概要：

- `telegram-allowed-chat-ids`：chat 级准入
- 管理员：始终通过用户级校验
- 黑名单：优先级最高
- 白名单模式开启时：仅管理员 + 白名单用户可用
- 白名单模式关闭时：所有未被 ban 用户可用
- `inline query` 没有 chat 上下文，因此只能校验**用户级权限**，不能套用 `telegram-allowed-chat-ids`

## 关键配置项

- `telegram-admin-user-ids`：管理员 `user_id` 列表
- `telegram-user-whitelist-enabled`：用户白名单模式默认开关
- `telegram-forward-chat-id`：歌曲归档群 `chat_id`
- `telegram-forward-enabled`：歌曲实时归档默认开关
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

说明：

- `telegram-state-file` 会保存运行时状态，例如：
  - 白名单模式开关
  - 白名单 / 黑名单用户集合
  - 归档转发开关
  - 待恢复面板 / 队列 / inflight 任务
- `telegram-cache-file` 的 `items` 现在会额外保存歌曲 caption/归档所需的元数据（专辑、风格、storefront、创建时间等），旧缓存 JSON 仍兼容读取

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
