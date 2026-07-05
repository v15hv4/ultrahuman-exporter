# syntax=docker/dockerfile:1.7

FROM golang:1.24-alpine AS builder

RUN apk add --no-cache build-base sqlite-dev

WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -o /out/ultrahuman-exporter ./src

FROM alpine:3.22

RUN apk add --no-cache ca-certificates sqlite-libs tzdata

WORKDIR /app
COPY --from=builder /out/ultrahuman-exporter /usr/local/bin/ultrahuman-exporter

ENTRYPOINT ["ultrahuman-exporter"]
