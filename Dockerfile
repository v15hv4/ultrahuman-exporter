FROM golang:1.24-alpine AS builder

RUN apk add --no-cache build-base sqlite-dev

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -o /out/ultrahuman-exporter ./src

FROM alpine:3.22

RUN apk add --no-cache ca-certificates sqlite-libs

WORKDIR /app
COPY --from=builder /out/ultrahuman-exporter /usr/local/bin/ultrahuman-exporter

ENTRYPOINT ["ultrahuman-exporter"]
