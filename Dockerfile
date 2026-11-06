# The Go stage runs on the build machine's own architecture and cross-compiles for the
# target. Only the small runtime stage runs under emulation for arm64, which keeps the
# multi-arch build fast.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd

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
