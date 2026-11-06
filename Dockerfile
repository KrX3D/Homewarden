# The web interface is plain static files, so it is built once on the build machine's
# own architecture no matter which platform the image is for.
FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# Vite writes into ../internal/web/dist, the Go embed directory.
RUN mkdir -p /internal/web/dist && npm run build


# The Go stage runs on the build machine's own architecture and cross-compiles for the
# target. Only the small runtime stage runs under emulation for arm64, which keeps the
# multi-arch build fast.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY integrations ./integrations
COPY --from=frontend /internal/web/dist ./internal/web/dist

ARG TARGETOS
ARG TARGETARCH
# Stamped by CI so a running container can be traced back to exactly what built it.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
        -trimpath \
        -ldflags "-s -w \
            -X main.version=${VERSION} \
            -X main.commit=${COMMIT} \
            -X main.buildDate=${BUILD_DATE}" \
        -o /out/homewarden ./cmd/homewarden


FROM alpine:3.22

# ca-certificates: every registry, GitHub and vendor API call is TLS.
# tzdata: schedules run in the user's local time (set TZ).
# su-exec: the entrypoint drops from root to PUID:PGID.
RUN apk add --no-cache ca-certificates tzdata su-exec

COPY --from=build /out/homewarden /usr/local/bin/homewarden
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV HOMEWARDEN_ADDR=:8080 \
    HOMEWARDEN_DATA=/data \
    PUID=99 \
    PGID=100
VOLUME /data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/homewarden"]
