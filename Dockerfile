# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.26.8-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG BUILD_ARCH
ARG BUILD_VERSION=dev
ARG BUILD_SOURCE=unknown
ARG SOURCE_DATE_EPOCH
RUN --mount=type=secret,id=proxy_ca \
    if [ -f /run/secrets/proxy_ca ]; then export SSL_CERT_FILE=/run/secrets/proxy_ca; fi && \
    mkdir -p /out/data/housefold && \
    case "${BUILD_ARCH}" in \
      amd64) goarch=amd64 ;; \
      aarch64) goarch=arm64 ;; \
      *) echo "unsupported BUILD_ARCH: ${BUILD_ARCH}" >&2; exit 1 ;; \
    esac && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" \
      go build -trimpath -buildvcs=false -ldflags="-s -w -X main.buildVersion=${BUILD_VERSION} -X main.buildSource=${BUILD_SOURCE}" \
      -o /out/housefold-runtime ./cmd/runtime && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/module-launcher ./cmd/module-launcher

FROM scratch
ARG BUILD_ARCH
ARG BUILD_VERSION=dev
ARG BUILD_SOURCE=unknown
LABEL io.hass.version="${BUILD_VERSION}" \
      io.hass.type="app" \
      io.hass.arch="${BUILD_ARCH}" \
      org.opencontainers.image.version="${BUILD_VERSION}" \
      org.opencontainers.image.revision="${BUILD_SOURCE}" \
      org.opencontainers.image.source="https://github.com/Housefold/runtime"
COPY --from=build /out/housefold-runtime /housefold-runtime
COPY --from=build /out/module-launcher /module-launcher
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=10001:10001 /out/data/ /data/
# Only the bounded Go bootstrap runs as root; it drops to UID/GID 10001 before listening.
USER 0:0
EXPOSE 8099/tcp
ENTRYPOINT ["/housefold-runtime"]
