## Multi-stage build for alloy-distributor (loki-producer) and pulse-loki-canary
## Build args allow multi-arch builds via Docker buildx.
ARG GO_VERSION=1.22
ARG ALPINE_VERSION=3.19

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /workspace

# Install build tooling (git needed for module fetching, ca-certs for https, build-base for CGO off possibility)
RUN apk add --no-cache git ca-certificates build-base

# Leverage go module cache
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Set build parameters
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

## Common ldflags: strip debug symbols and set buildinfo variables (package internal/buildinfo)
ARG BUILD_VERSION=dev
ARG BUILD_DATE=unknown
ARG BUILD_COMMIT=unknown
ENV LDFLAGS="-s -w -X github.com/DeveloperDarkhan/loki-producer/internal/buildinfo.Version=${BUILD_VERSION} -X github.com/DeveloperDarkhan/loki-producer/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/DeveloperDarkhan/loki-producer/internal/buildinfo.Date=${BUILD_DATE}"

# Build binaries
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags "$LDFLAGS" -o /workspace/bin/alloy-distributor ./cmd/alloy-distributor && \
    go build -ldflags "$LDFLAGS" -o /workspace/bin/pulse-loki-canary ./cmd/pulse-loki-canary

# Final minimal runtime image
FROM alpine:${ALPINE_VERSION}
RUN adduser -D -u 10001 app && \
    apk add --no-cache ca-certificates tzdata && \
    mkdir -p /config
USER 10001
WORKDIR /
COPY --from=build /workspace/bin/alloy-distributor /bin/alloy-distributor
COPY --from=build /workspace/bin/pulse-loki-canary /bin/pulse-loki-canary

EXPOSE 3101

# Default entrypoint runs the distributor; Job can override command to run canary
ENTRYPOINT ["/bin/alloy-distributor"]
CMD ["-config.file=/config/config.yaml"]
