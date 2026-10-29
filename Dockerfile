ARG GOVERSION=1.26.10

FROM --platform=$BUILDPLATFORM golang:${GOVERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/apple-music-dl . && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/mp4decrypt-fallback ./cmd/mp4decrypt-fallback

# ✅ mp4decrypt 下载放在独立阶段，curl/unzip 不进入运行镜像：
#   - amd64/x86_64: 使用 Bento4 官方二进制
#   - arm64 及其他架构: 留空，运行镜像里回退到内置 Go 版 fallback
FROM --platform=$BUILDPLATFORM alpine:3.20 AS bento4
ARG TARGETARCH
ARG BENTO4_VERSION=1-6-0-641
RUN set -eux; \
    mkdir -p /out; \
    if [ "${TARGETARCH}" = "amd64" ]; then \
      apk add --no-cache curl unzip; \
      curl -fsSL "https://www.bok.net/Bento4/binaries/Bento4-SDK-${BENTO4_VERSION}.x86_64-unknown-linux.zip" -o /tmp/bento4.zip; \
      unzip -j /tmp/bento4.zip "*/bin/mp4decrypt" -d /out; \
      chmod +x /out/mp4decrypt; \
      rm -f /tmp/bento4.zip; \
    fi

FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive

# ✅ 安装依赖：ffmpeg + ca证书 + MP4Box(gpac)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    ca-certificates \
    gpac \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/apple-music-dl /usr/local/bin/apple-music-dl
COPY --from=builder /bin/mp4decrypt-fallback /usr/local/bin/mp4decrypt-fallback
COPY --from=bento4 /out/ /usr/local/bin/
RUN if [ ! -x /usr/local/bin/mp4decrypt ]; then \
      ln -sf /usr/local/bin/mp4decrypt-fallback /usr/local/bin/mp4decrypt; \
    fi

WORKDIR /app
COPY config.example.yaml ./config.yaml
RUN echo 'alac-save-folder: "/downloads/ALAC"' >> config.yaml \
    && echo 'atmos-save-folder: "/downloads/Atmos"' >> config.yaml \
    && echo 'aac-save-folder: "/downloads/AAC"' >> config.yaml

ENTRYPOINT ["/usr/local/bin/apple-music-dl"]
