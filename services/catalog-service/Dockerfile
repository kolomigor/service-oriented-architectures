FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY gen ./gen
COPY services/catalog-service ./services/catalog-service

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/catalog-service \
    ./services/catalog-service/src

FROM alpine:3.22

RUN addgroup -S app && adduser -S -G app app

COPY --from=builder /out/catalog-service /app/catalog-service

USER app
EXPOSE 8080 50051

ENTRYPOINT ["/app/catalog-service"]
