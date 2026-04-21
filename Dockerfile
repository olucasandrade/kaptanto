# syntax=docker/dockerfile:1

# Build stage — produces a static binary with no CGO.
FROM golang:1.25-alpine AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w \
        -X 'github.com/kaptanto/kaptanto/internal/version.Version=${VERSION}' \
        -X 'github.com/kaptanto/kaptanto/internal/version.Commit=${COMMIT}' \
        -X 'github.com/kaptanto/kaptanto/internal/version.BuildDate=${BUILD_DATE}'" \
      -o /kaptanto ./cmd/kaptanto

# Runtime stage — distroless for minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /kaptanto /kaptanto

ENTRYPOINT ["/kaptanto"]
