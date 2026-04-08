[English](./README.md) / 简体中文

# Apple Music Telegram Bot（Go）

本仓库是从原项目提取出来的 **Telegram Bot 独立版**。
已移除 AstrBot API 模式与相关 HTTP 服务能力。

## 功能概览

- Telegram 机器人下载能力（保留）：
  - `song`
  - `album`
  - `playlist`
  - `station`
  - `music-video`
- 支持逐个发送 / ZIP 发送
- 支持 Telegram `file_id` 缓存复用（song audio + MV + ZIP）
- 支持任务队列、worker 并发、重试/限速与 `retry_after` 背压
- 支持运行时状态快照恢复、下载目录后台 janitor 清理
- 保留 `/settings` 配置能力（格式、歌词、封面、worker 等）

## 依赖要求

- [MP4Box](https://gpac.io/downloads/gpac-nightly-builds/)
- [wrapper](https://github.com/WorldObservationLog/wrapper)（需运行）
- [mp4decrypt](https://www.bento4.com/downloads/)（MV 必需）
- `ffmpeg`（动态封面与可选转码需要）

## 快速开始

1. 复制配置模板：

   ```bash
   cp config.example.yaml config.yaml
   ```

2. 设置 Bot Token（任选其一）：

   - `config.yaml` 中设置 `telegram-bot-token`
   - 环境变量 `TELEGRAM_BOT_TOKEN`

3. （可选）限制可用 chat：

   - `telegram-allowed-chat-ids: [123456789]`

4. 启动机器人：

   ```bash
   go run . --bot
   ```

## Docker

构建镜像：

```bash
docker build -t applemusic-telegram-bot .
```

运行机器人：

```bash
docker run --rm -it \
  -v "$PWD/config.yaml":/app/config.yaml \
  -v "$PWD/downloads":/downloads \
  -v "$PWD/telegram-cache.json":/app/telegram-cache.json \
  -e TELEGRAM_BOT_TOKEN=你的BotToken \
  applemusic-telegram-bot --bot
```

## 机器人命令

- `/h` 帮助
- `/i` 查看 chat_id 或按资源 ID 下载
- `/sg <关键词>` 搜索歌曲
- `/sa <关键词>` 搜索专辑
- `/sr <关键词>` 搜索艺人
- `/s <type> <关键词>` 统一搜索（`song|album|artist`）
- `/u <apple-music-url>` 解析并下载
- `/rf <apple-music-url>` 强制重下
- `/ap <artist-url|artist-id>` 导出艺人相关资源
- `/cv <url|type id>` 仅下载封面
- `/ac <url|type id>` 仅下载动态封面
- `/ly <song/album target>` 导出歌词
- `/st [alac|flac|aac|atmos|...|songzip|worker1..worker4]`

旧命令别名仍兼容。

## 运行说明（Telegram）

- `song` 支持逐个/ZIP 发送（由设置控制）。
- ZIP 超限会自动回退逐个发送。
- 遇到 `retry_after` 会触发全局上传背压。
- 运行时状态会写入 `telegram-state-file`，重启后可恢复。
- 清理由后台 janitor 负责，关键配置：
  - `telegram-download-max-gb`
  - `telegram-cleanup-interval-sec`
  - `telegram-cleanup-scan-interval-sec`
  - `telegram-cleanup-protect-sec`

## 安全建议

- 不要提交包含真实密钥的 `config.yaml`。
- `telegram-api-url` 优先使用 `https://`。
- 如使用明文 HTTP 的自建 Telegram Bot API，仅在可信网络内使用。
