FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
RUN mkdir -p internal/api && go run -modfile=api/generator.mod -mod=mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config api/codegen.yaml api/openapi.yaml
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bootstrap ./cmd/bootstrap

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S marketplace && adduser -S marketplace -G marketplace
WORKDIR /app
COPY --from=build /server /app/server
COPY --from=build /bootstrap /app/bootstrap
USER marketplace
EXPOSE 8080
ENTRYPOINT ["/app/server"]
