FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY gen ./gen
COPY services/recommendation-service ./services/recommendation-service

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/recommendation-service \
    ./services/recommendation-service/src

FROM alpine:3.22

RUN addgroup -S app && adduser -S -G app app

COPY --from=builder /out/recommendation-service /app/recommendation-service

USER app
EXPOSE 8080 50053

ENTRYPOINT ["/app/recommendation-service"]
