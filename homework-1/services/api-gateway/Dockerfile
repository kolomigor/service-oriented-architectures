FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY gen ./gen
COPY services/api-gateway ./services/api-gateway

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/api-gateway \
    ./services/api-gateway/src

FROM alpine:3.22

RUN addgroup -S app && adduser -S -G app app

COPY --from=builder /out/api-gateway /app/api-gateway

USER app
EXPOSE 8080 50052

ENTRYPOINT ["/app/api-gateway"]
