FROM --platform=$BUILDPLATFORM golang:1.25.10-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-X main.version=${VERSION}" -o /out/engram ./cmd/engram

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 10001 engram \
    && mkdir -p /data \
    && chown -R engram:engram /data
USER engram
WORKDIR /home/engram
COPY --from=builder /out/engram /usr/local/bin/engram

# /data is chowned to the engram user (10001) above so a fresh named volume
# mounted here inherits writable ownership on first creation. See
# docs/engram-http/README.md for the "adopt an existing data dir" bind-mount
# path, which needs the same uid 10001 ownership on the host directory.
ENV ENGRAM_DATA_DIR=/data
EXPOSE 7438

# alpine ships busybox wget; no extra package needed for this liveness probe.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -q -O /dev/null http://127.0.0.1:7438/health || exit 1

ENTRYPOINT ["engram"]
CMD ["mcp", "--transport=http", "--listen=0.0.0.0:7438"]
