FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /login ./cmd/login
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /inspector ./cmd/inspector

FROM alpine:3.19 AS chrome-base

RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ttf-freefont \
    ca-certificates \
    tzdata

ENV CHROME_BIN=/usr/bin/chromium-browser
ENV CHROME_PATH=/usr/lib/chromium/

FROM chrome-base AS runtime

RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

COPY --from=builder /login /app/login
COPY --from=builder /inspector /app/inspector

RUN mkdir -p /tmp/chrome-linux && ln -s /usr/lib/chromium /tmp/chrome-linux/chrome

RUN chown -R appuser:appgroup /app

USER appuser

ENV PORT=8080
ENV GIN_MODE=release

EXPOSE 8080

ENTRYPOINT ["/app/login"]
