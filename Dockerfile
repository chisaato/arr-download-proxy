# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.7-alpine3.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/arr-download-proxy ./cmd/server

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 app \
    && adduser -D -H -u 10001 -G app app \
    && mkdir -p /app/data /config \
    && chown 10001:10001 /app/data

COPY --from=build /out/arr-download-proxy /usr/local/bin/arr-download-proxy
WORKDIR /app
USER 1000:1000
EXPOSE 9091
ENTRYPOINT ["/usr/local/bin/arr-download-proxy"]
CMD ["--config", "/config/config.yaml"]
