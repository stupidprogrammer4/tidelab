.PHONY: build test vet demo

build:
	go build -o bin/tidelab ./cmd/tidelab

test:
	go test ./...

vet:
	go vet ./...

demo:
	go run ./cmd/tidelab version
	go run ./cmd/tidelab config check
