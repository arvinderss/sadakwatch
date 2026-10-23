# Builds both Go binaries; the image runs the API by default.
#   docker build -t sadakwatch .
#   docker run -p 8080:8080 sadakwatch                       # API + dashboard (in-memory)
#   docker run sadakwatch /usr/local/bin/edge-agent          # edge agent
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/edge-agent ./cmd/edge-agent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 sadak && mkdir -p /data && chown sadak /data
COPY --from=build /out/api /out/edge-agent /usr/local/bin/
USER sadak
ENV HTTP_ADDR=:8080 STORAGE_PATH=/data/media EDGE_DATA_DIR=/data/edge
EXPOSE 8080
CMD ["/usr/local/bin/api"]
