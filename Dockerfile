# 漫剧工坊：stage1 构建前端，stage2 编译 Go（无 CGO），stage3 运行镜像（带 ffmpeg 与中文字体，用于合成成片和烧录字幕）
FROM --platform=$BUILDPLATFORM node:22-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY colab/ ./colab/
COPY docs/antigravity/ ./docs/antigravity/
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/manju-studio .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata ffmpeg font-noto-cjk fontconfig && mkdir -p /data
ENV TZ=Asia/Shanghai DATA_DIR=/data PORT=8080
COPY --from=build /out/manju-studio /usr/local/bin/manju-studio
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/manju-studio"]
