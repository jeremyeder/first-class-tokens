# syntax=docker/dockerfile:1.7
# The controller is always built for the amd64 OpenShift worker pool.

FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS builder

ARG CONTROLLER_PACKAGE=./cmd/manager
ARG SOURCE_REVISION=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Keep dependency resolution in its own layer so source-only rebuilds are cheap.
COPY go.mod go.sum ./
RUN go mod download

COPY api ./api
COPY cmd/manager ./cmd/manager
COPY internal ./internal

ENV CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH

RUN go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/intake-controller \
      "${CONTROLLER_PACKAGE}"

FROM --platform=linux/amd64 gcr.io/distroless/static-debian12:nonroot

ARG SOURCE_REVISION=unknown

LABEL org.opencontainers.image.title="first-class-tokens intake controller" \
      org.opencontainers.image.source="https://github.com/jeder/first-class-tokens" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"

COPY --from=builder /out/intake-controller /usr/local/bin/intake-controller

EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/intake-controller"]
