# syntax=docker/dockerfile:1.7

FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
RUN apk add --no-cache build-base ca-certificates git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" GOOS=linux \
    go build -tags sqlite_fts5 -trimpath -ldflags="-s -w" -o /out/wacli ./cmd/wacli

FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && adduser -D -u 10001 -h /home/wacli wacli \
    && mkdir -p /data/store /data/state /data/config /data/cache \
    && chown -R wacli:wacli /data
ENV HOME=/home/wacli \
    WACLI_STORE_DIR=/data/store \
    XDG_STATE_HOME=/data/state \
    XDG_CONFIG_HOME=/data/config \
    XDG_CACHE_HOME=/data/cache
VOLUME ["/data"]
WORKDIR /data
COPY --from=build /out/wacli /usr/local/bin/wacli
USER wacli
ENTRYPOINT ["wacli"]
CMD ["--help"]
