# syntax=docker/dockerfile:1
FROM golang:1.27 AS builder
ARG VERSION
WORKDIR /build
ADD go.mod /build/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux \
       go mod download
ADD . /build/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux \
    go build \
        -trimpath \
        -ldflags "-s -X jokertc/common.Version=${VERSION}" \
        -v \
        -o jokertc \
    cmd/cli/main.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/jokertc /app/jokertc
CMD ["/app/jokertc"]
