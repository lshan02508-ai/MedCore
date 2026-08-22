.PHONY: build test race run mock fmt

build:
	go build ./cmd/gateway ./cmd/mock-backend

test:
	go test ./...

race:
	go test -race ./...

run:
	go run ./cmd/gateway -config configs/gateway.json

mock:
	go run ./cmd/mock-backend

fmt:
	gofmt -w $$(find . -name '*.go' -type f)
