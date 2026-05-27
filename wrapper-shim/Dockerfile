ARG GOVERSION=1.23

FROM --platform=$BUILDPLATFORM golang:${GOVERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY . .

# `go mod tidy` resolves the dependency graph the first time the image is
# built (no go.sum is checked in). Subsequent builds reuse the module cache
# layer Docker already has.
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" \
    -o /out/wrapper-shim ./cmd/wrapper-shim

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/wrapper-shim /usr/local/bin/wrapper-shim

# Default ports match the original wrapper so the bot's existing config
# (decrypt-m3u8-port: ":10020", get-m3u8-port: ":20020") keeps working.
EXPOSE 10020/tcp
EXPOSE 20020/tcp

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/wrapper-shim"]
CMD ["-manager", "wrapper-manager:8080", "-m3u8-listen", ":20020", "-decrypt-listen", ":10020"]
