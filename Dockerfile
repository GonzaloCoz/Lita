# Stage 1: Build Go binary
FROM golang:1.24-alpine AS builder
WORKDIR /app

COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/lita-server ./cmd/server

# Stage 2: Minimal runtime image with CA certificates
FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/lita-server /app/lita-server
COPY web/ /app/web/

EXPOSE 8080
ENV PORT=8080

CMD ["/app/lita-server"]
