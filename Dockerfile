# syntax=docker/dockerfile:1

# Frontend and Go builds run on the build machine's architecture and cross-compile,
# so multi-arch images don't need emulation for the slow steps.
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
COPY internal/webui/dist/README.md ../internal/webui/dist/README.md
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/nvr ./cmd/nvr

FROM alpine:3.24
# ffmpeg: thumbnail frame grabs and resizing. tzdata: camera-local file name times.
RUN apk add --no-cache ffmpeg tzdata ca-certificates \
    && addgroup -S -g 1000 nvr && adduser -S -u 1000 -G nvr -h /data nvr \
    && mkdir -p /data && chown nvr:nvr /data
COPY --from=go /out/nvr /usr/local/bin/nvr
USER nvr
ENV DATA_DIR=/data PORT=8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/nvr"]
