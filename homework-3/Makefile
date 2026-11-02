export PATH := $(shell go env GOPATH)/bin:$(PATH)

.PHONY: generate

generate:
	mkdir -p gen
	protoc -I proto \
		--go_out=gen --go_opt=paths=source_relative \
		--go-grpc_out=gen --go-grpc_opt=paths=source_relative \
		proto/flight/v1/flight.proto
