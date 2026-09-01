# Apple Music Telegram Bot (Go)

面向 Telegram 场景的 Apple Music 下载机器人，支持歌曲、专辑、播放列表、电台、MV、艺人和策展人页面，并提供任务队列、缓存复用、运行时恢复、用户权限与订阅监控。

示例 Bot：[@jellyamdl_bot](https://t.me/jellyamdl_bot)

## 项目关系

当前仓库：[`wuuduf/applemusic-telegram-bot`](https://github.com/wuuduf/applemusic-telegram-bot)

继承链：

1. `alacleaker/apple-music-alac-downloader`
2. `zhaarey/apple-music-downloader`
3. `moeleak/apple-music-downloader-bot`
4. `wuuduf/applemusic-telegram-bot`（本仓库）

本仓库在上游基础上继续演进，重点是 Telegram 运行时稳定性、队列恢复、管理能力，以及单账号 `wrapper` / 多账号 `wrapper-manager` 两种部署方式。

## 核心能力

- 下载类型：`song` / `album` / `playlist` / `station` / `music-video`
- 艺人处理：全部专辑、全部歌曲、MV、主页图、专辑封面和动态封面
- 策展人处理：展开策展页面中的专辑列表
- 音频格式：ALAC、Atmos、AAC 及可选的 FFmpeg 后处理
- 扩展输出：静态封面、动态封面、LRC/TTML 歌词、歌曲赏析
- Telegram 发送：逐个发送或 ZIP，文件超限时自动回退
- Telegram `file_id` 缓存：音频、视频和 ZIP 可直接复用
- 任务系统：1～4 Worker、队列查询、单任务取消、状态快照恢复
- 稳定性：`retry_after` 全局背压、资源守卫、目录配额清理、错误日志轮转、每日自动重启
- 权限系统：chat 白名单、管理员、用户白名单模式和黑名单
- 归档系统：歌曲实时归档、缓存音频批量转存
- 艺人订阅：检测新专辑、临时发行记录及正式刷新下载
- Wrapper 后端：支持旧单账号 `wrapper`，也支持多账号 `wrapper-manager + wrapper-shim`

## 运行依赖

### 源码运行

- Go `1.25.0`，推荐使用 `go.mod` 指定的 toolchain `1.25.10`
- [wrapper](https://github.com/WorldObservationLog/wrapper)，或 [wrapper-manager](https://github.com/WorldObservationLog/wrapper-manager) + 本仓库的 `wrapper-shim`
- [MP4Box / GPAC](https://gpac.io/downloads/gpac-nightly-builds/)
- [mp4decrypt / Bento4](https://www.bento4.com/downloads/)（MV）；非 amd64 Docker 镜像会使用仓库内置 Go fallback
- `ffmpeg`（FLAC、动态封面及可选转码）

### Docker Compose

Docker 镜像已经安装 Bot 所需的 GPAC、FFmpeg 和解密工具；宿主机主要需要：

- Docker Engine / Docker Desktop
- Docker Compose v2（`docker compose`）
- `make`（仅 `wrapper-manager` 账号管理快捷命令需要）

## 快速开始（源码运行）

### 1. 准备配置

```bash
cp config.example.yaml config.yaml
```

至少设置：

- `telegram-bot-token`，或环境变量 `TELEGRAM_BOT_TOKEN`
- `media-user-token`（歌词、AAC-LC、Station、MV 等功能需要）
- 可访问的 `decrypt-m3u8-port` 与 `get-m3u8-port`

`authorization-token` 通常无需填写，程序会自动从 Apple Music Web 获取并缓存临时 Token。

常用可选配置：

- `storefront`：默认搜索区，例如 `us` / `jp`
- `telegram-allowed-chat-ids`：允许使用 Bot 的 chat
- `telegram-admin-user-ids`：管理员 user ID
- `telegram-forward-chat-id`：归档群 chat ID
- `telegram-proxy-url` / `telegram-no-proxy`：Telegram API 网络策略

完整字段见 [`config.example.yaml`](./config.example.yaml)。

### 2. 启动 Telegram Bot

先单独启动并登录 `wrapper` 或 `wrapper-manager`，然后运行：

```bash
go run . --bot
```

### 3. CLI 模式

```bash
# 下载专辑
go run . 'https://music.apple.com/us/album/.../1234567890'

# 下载单曲
go run . --song 'https://music.apple.com/us/song/.../1234567890'

# 搜索
go run . --search song 'Taylor Swift'
```

可用 CLI 参数以源码注册为准：

```bash
go run . --help
```

## Telegram 命令

### 常用命令

- `/h`：帮助
- `/i`：查看当前 `chat_id`；也可按资源 ID 下载
- `/sg <关键词>`：搜索歌曲
- `/dg <歌名或 歌手 - 歌名>`：点歌，直接下载第一个匹配歌曲（无需选择面板）
- `/sa <关键词>`：搜索专辑
- `/sr <关键词>`：搜索艺人
- `/s <song|album|artist> <关键词>`：统一搜索
- `/u <Apple Music 链接...>`：解析并下载一个或多个链接
- `/rf <Apple Music 链接>`：清除缓存并强制重新下载、上传
- `/ap <artist-url|artist-id>`：导出艺人主页图、专辑封面和动态封面
- `/cv <url|type id>`：仅下载静态封面
- `/ac <url|type id>`：仅下载动态封面
- `/ly <song|album target>`：导出歌词文件
- `/status`：查看全局队列、Worker 和运行指标
- `/queue`：查看当前 chat 的排队与运行任务
- `/cancel <request_id>`：取消当前 chat 的指定任务
- `/st [value]`：查看或修改格式、歌词、语言、ZIP、Worker、自动附件和歌曲赏析设置
- `/amwhoami`：查看当前 `user_id / chat_id`

### ID 与批量下载

- `/songid`
- `/albumid`
- `/playlistid`
- `/stationid`
- `/mvid`
- `/artistid`
- `/id <song|album|playlist|station|mv|artist|curator> <id...>`

支持在一条消息中发送多个 Apple Music 链接；多个 ID 可使用空格、英文/中文逗号或英文/中文分号分隔。

```text
/songid 123 456 789
/albumid 111,222,333
/id curator 1702073195

https://music.apple.com/us/album/.../123
https://music.apple.com/us/playlist/.../pl.xxxxx
https://music.apple.com/us/curator/100-best-albums/1702073195
```

### 管理员与订阅命令

- `/amadmin`：管理员面板
- `/amwlon` / `/amwloff`：开启/关闭用户白名单模式
- `/amwladd <user_id>` / `/amwldel <user_id>`：添加/移除白名单用户
- `/amban <user_id>` / `/amunban <user_id>`：封禁/解封用户
- `/amcachepush`：将音频缓存按顺序转存到归档群
- `/sub artist <artist-url|artist-id>`：订阅艺人新专辑
- `/sub list [enabled|paused]`：查看订阅
- `/sub pause|resume|del <subscription_id>`：暂停、恢复或删除订阅
- `/subtemp [list|pending|ready|refreshed|artist <关键词>|album <关键词>]`：查看临时发行记录
- `/subrefresh <temporary_release_id>`：正式刷新单个临时发行
- `/subrefreshall`：刷新所有已到期临时发行

`/amwladd`、`/amwldel`、`/amban`、`/amunban` 也支持回复目标用户消息后不带 `user_id` 执行。

权限顺序：

1. `telegram-allowed-chat-ids` 进行 chat 级准入
2. 管理员始终通过用户级校验
3. 黑名单优先于普通白名单
4. 白名单模式开启时，仅管理员和白名单用户可用
5. Inline Query 没有 chat 上下文，只执行用户级权限校验

## 关键运行配置

### Telegram 与权限

- `telegram-bot-token`
- `telegram-api-url`
- `telegram-proxy-url` / `telegram-no-proxy`
- `telegram-allowed-chat-ids`
- `telegram-admin-user-ids`
- `telegram-user-whitelist-enabled`
- `telegram-forward-chat-id` / `telegram-forward-enabled`
- `lastfm-api-key`：启用歌曲赏析来源

### 缓存、恢复和监控

- `telegram-cache-file`：Telegram `file_id` 与媒体元数据缓存
- `telegram-state-file`：权限状态、面板、排队和 inflight 任务快照
- `telegram-metrics-interval-sec`
- `telegram-metrics-listen-addr`
- `telegram-daily-restart-enabled`
- `telegram-subscription-check-interval-sec`

### 资源与清理

- `telegram-download-max-gb`
- `telegram-cleanup-interval-sec`
- `telegram-cleanup-scan-interval-sec`
- `telegram-cleanup-protect-sec`
- `telegram-resource-check-interval-sec`
- `telegram-min-free-disk-mb`
- `telegram-min-free-tmp-mb`
- `telegram-max-memory-mb`

`telegram-cache-file` 和 `telegram-state-file` 使用临时文件 + rename 的原子写入方式。Docker 中应挂载它们所在的**目录**，不要把 JSON 文件作为单文件 bind mount，否则可能出现 `device or resource busy`。

## Docker Compose 部署

Compose 提供两套互斥的 Wrapper 数据面：

| 模式 | Compose profile | 用途 | Bot 连接地址 |
| --- | --- | --- | --- |
| 单账号 | `wrapper` | 兼容旧版部署 | `wrapper:10020` / `wrapper:20020` |
| 多账号 | `manager` | `wrapper-manager` 自动分配账号 | `wrapper-shim:10020` / `wrapper-shim:20020` |

`telegram-bot-api` 和 `bot` 没有 profile，选择任意 Wrapper profile 时都会一起启动。

### 1. 克隆并准备公共配置

```bash
git clone https://github.com/wuuduf/applemusic-telegram-bot.git
cd applemusic-telegram-bot
cp config.example.yaml config.yaml
mkdir -p data/telegram-bot-api downloads bot-runtime
```

编辑 `config.yaml`：

```yaml
telegram-bot-token: "你的 Bot Token"
telegram-api-url: "http://telegram-bot-api:8081"
telegram-cache-file: "/app/runtime/telegram-cache.json"
```

同时修改 `docker-compose.yml` 中：

- `telegram-bot-api.environment.TELEGRAM_API_ID`
- `telegram-bot-api.environment.TELEGRAM_API_HASH`
- 如不把 Token 写进 `config.yaml`，可填写 `bot.environment.TELEGRAM_BOT_TOKEN`

容器之间必须使用 Compose **service name**，不能使用 `127.0.0.1`。如果你自行把服务改名为 `bot-3`、`telegram-bot-api-3` 等，`depends_on`、配置地址和网络别名也必须同步。

### 2A. 单账号 wrapper 模式

检查宿主机架构：

```bash
uname -m
```

按架构修改 `wrapper-init` 和 `wrapper` 的 `image` / `platform`：

- `aarch64` / `arm64`：`jelly714love/wrapper:arm64` + `linux/arm64`
- `x86_64` / `amd64`：`jelly714love/wrapper:amd64` + `linux/amd64`

创建数据目录，并在 `docker-compose.yml` 的 `wrapper-init.environment.args` 填写 Apple ID 登录参数：

```bash
mkdir -p rootfs/data
```

```yaml
decrypt-m3u8-port: "wrapper:10020"
get-m3u8-port: "wrapper:20020"
```

首次初始化：

```bash
docker compose --profile init run --rm wrapper-init
```

如果日志要求 2FA，在另一个终端写入当前六位验证码：

```bash
echo -n 240020 > rootfs/data/data/com.apple.android.music/files/2fa.txt
```

启动完整单账号方案：

```bash
docker compose --profile wrapper up -d --build
```

### 2B. 多账号 wrapper-manager 模式

`docker-compose.yml` 从兄弟目录 `../wrapper-manager` 构建 manager。Manager 为创建隔离实例需要 PID namespace/chroot 能力，当前 Compose 会以 `privileged: true` 并添加 `SYS_ADMIN` 启动；请仅部署在你信任的主机。然后先克隆源码：

```bash
cd ..
git clone https://github.com/WorldObservationLog/wrapper-manager.git
cd applemusic-telegram-bot
mkdir -p wrapper-manager-data
```

目录关系必须是：

```text
parent/
├── applemusic-telegram-bot/
└── wrapper-manager/
```

`config.yaml` 使用 shim 的兼容 TCP 端口：

```yaml
decrypt-m3u8-port: "wrapper-shim:10020"
get-m3u8-port: "wrapper-shim:20020"
```

先启动 manager：

```bash
docker compose --profile manager up -d --build
```

然后登录至少一个 Apple ID：

```bash
make login
make accounts
```

账号管理快捷命令：

```bash
make login-one USER=me@example.com
make login-batch FILE=accounts.tsv
make logout USERS="a@example.com b@example.com"
make accounts
```

批量文件格式为每行 `AppleID<TAB>密码`；2FA 仍在终端交互输入。该文件包含明文密码，建议执行 `chmod 600 accounts.tsv`，并且不要提交到 Git。

数据链路：

```text
bot ──TCP──> wrapper-shim ──gRPC──> wrapper-manager ──> wrapper instances
```

Shim 只负责 Bot 使用的 M3U8/Decrypt 数据面；登录、登出和状态查询由 `wmcli` / Makefile 完成。详细协议与限制见：

- [`wrapper-shim/README.md`](./wrapper-shim/README.md)
- [`tools/wrapper-manager-cli/README.md`](./tools/wrapper-manager-cli/README.md)

### 3. 查看状态与日志

```bash
docker compose ps
docker compose logs -f bot
```

多账号模式还可以查看：

```bash
docker compose logs -f wrapper-manager wrapper-shim
make accounts
```

### 4. 仅重建 Bot

不重建 Wrapper 或 Telegram Bot API：

```bash
docker compose build bot
docker compose up -d --no-deps --force-recreate bot
```

只重启、不重新构建：

```bash
docker compose restart bot
```

### 5. 在两种 Wrapper 模式之间切换

先停止当前 Compose 项目（显式启用两个 profile，避免遗留容器），再修改 `config.yaml` 中的 Wrapper service name，最后启动目标 profile：

```bash
docker compose --profile wrapper --profile manager down

# 单账号
docker compose --profile wrapper up -d --build

# 或多账号
docker compose --profile manager up -d --build
```

不要同时运行两套 profile；它们提供相同语义的解密与 M3U8 服务。

## Docker 单容器模式（可选）

下面只启动 Bot；Wrapper 和 Telegram API 必须已经可从容器访问：

```bash
docker build -t applemusic-telegram-bot .

docker run --rm -it \
  --network host \
  -v "$PWD/config.yaml":/app/config.yaml:ro \
  -v "$PWD/downloads":/downloads \
  -v "$PWD/bot-runtime":/app/runtime \
  -e TELEGRAM_BOT_TOKEN=your_bot_token \
  applemusic-telegram-bot --bot
```

## 项目结构

```text
applemusic-telegram-bot/
├── main.go                         # 根 CLI/Bot 入口
├── cmd/
│   ├── applemusic-telegram-bot/    # 可独立构建的应用入口
│   └── mp4decrypt-fallback/        # 非 amd64 Docker 解密 fallback
├── internal/
│   ├── app/                        # Telegram、任务队列、下载流水线
│   ├── catalog/                    # Apple Music Catalog/艺人/策展人访问
│   ├── cli/                        # CLI 会话与运行逻辑
│   └── storage/                    # 缓存与持久化辅助
├── utils/                          # Apple API、歌词、媒体处理等
├── wrapper-shim/                   # wrapper TCP 到 manager gRPC 的兼容层
├── tools/wrapper-manager-cli/      # manager 登录/登出/状态 CLI
├── docker-compose.yml
├── config.example.yaml
├── Makefile
└── Dockerfile
```

## 开发与验证

```bash
# Go 主项目
go test ./...
go build .

# wrapper-shim（与 Dockerfile 一样，首次先补齐模块校验文件）
cd wrapper-shim
go mod tidy
go test ./...
go build ./cmd/wrapper-shim

# wrapper-manager 管理 CLI 状态机测试
cd ..
make wmcli-test
```

## 安全提示

- 不要提交 `config.yaml`、`media-user-token`、Telegram Bot Token、Apple ID 密码或 `accounts.tsv`
- `wrapper-manager` 的 `8080` 端口默认只绑定 `127.0.0.1`
- `wrapper-shim` 不提供认证或 TLS，应只运行在受信任的 Docker 网络
- 本地 Telegram Bot API 使用 HTTP 时，只应通过受信任的容器网络访问

## 致谢

- [moeleak/apple-music-downloader-bot](https://github.com/moeleak/apple-music-downloader-bot)
- [zhaarey/apple-music-downloader](https://github.com/zhaarey/apple-music-downloader)
- [WorldObservationLog/wrapper](https://github.com/WorldObservationLog/wrapper)
- [WorldObservationLog/wrapper-manager](https://github.com/WorldObservationLog/wrapper-manager)
