# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG BUILD_ARCH
ARG BUILD_VERSION=dev
RUN mkdir -p /out && \
    case "${BUILD_ARCH}" in \
      amd64) goarch=amd64 ;; \
      aarch64) goarch=arm64 ;; \
      *) echo "unsupported BUILD_ARCH: ${BUILD_ARCH}" >&2; exit 1 ;; \
    esac && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" \
      go build -trimpath -ldflags="-s -w" \
      -o /out/housefold-runtime ./cmd/runtime

FROM scratch
ARG BUILD_ARCH
ARG BUILD_VERSION=dev
LABEL io.hass.version="${BUILD_VERSION}" \
      io.hass.type="app" \
      io.hass.arch="${BUILD_ARCH}"
COPY --from=build /out/housefold-runtime /housefold-runtime
USER 10001:10001
EXPOSE 8099/tcp
ENTRYPOINT ["/housefold-runtime"]
