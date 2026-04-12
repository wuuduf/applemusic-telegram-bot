ARG GOVERSION=1.25.5

FROM --platform=$BUILDPLATFORM golang:${GOVERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/apple-music-dl . && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/mp4decrypt-fallback ./cmd/mp4decrypt-fallback

FROM ubuntu:22.04
ARG TARGETARCH
ARG BENTO4_VERSION=1-6-0-641
ENV DEBIAN_FRONTEND=noninteractive

# ✅ 安装依赖：ffmpeg + ca证书 + MP4Box(gpac) + 下载工具
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    ca-certificates \
    gpac \
    curl \
    unzip \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/apple-music-dl /usr/local/bin/apple-music-dl
COPY --from=builder /bin/mp4decrypt-fallback /usr/local/bin/mp4decrypt-fallback

# ✅ mp4decrypt：
#   - amd64/x86_64: 使用 Bento4 官方二进制
#   - arm64 及其他架构: 自动回退到内置 Go 版 fallback
RUN set -eux; \
    if [ "${TARGETARCH}" = "amd64" ]; then \
      curl -fsSL "https://www.bok.net/Bento4/binaries/Bento4-SDK-${BENTO4_VERSION}.x86_64-unknown-linux.zip" -o /tmp/bento4.zip; \
      unzip -j /tmp/bento4.zip "*/bin/mp4decrypt" -d /usr/local/bin; \
      chmod +x /usr/local/bin/mp4decrypt; \
      rm -f /tmp/bento4.zip; \
    else \
      ln -sf /usr/local/bin/mp4decrypt-fallback /usr/local/bin/mp4decrypt; \
    fi

WORKDIR /app
COPY config.example.yaml ./config.yaml
RUN echo 'alac-save-folder: "/downloads/ALAC"' >> config.yaml \
    && echo 'atmos-save-folder: "/downloads/Atmos"' >> config.yaml \
    && echo 'aac-save-folder: "/downloads/AAC"' >> config.yaml

ENTRYPOINT ["/usr/local/bin/apple-music-dl"]
