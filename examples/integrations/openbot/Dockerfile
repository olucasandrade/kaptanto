# syntax=docker/dockerfile:1

FROM golang:1.26.9-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY examples/integrations/openbot/adapter ./examples/integrations/openbot/adapter
RUN CGO_ENABLED=0 go build -trimpath -o /openbot-adapter ./examples/integrations/openbot/adapter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /openbot-adapter /openbot-adapter
EXPOSE 8787
ENTRYPOINT ["/openbot-adapter"]
