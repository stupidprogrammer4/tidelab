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
	go run ./cmd/tidelab book inspect
	go run ./cmd/tidelab estimate --side buy --base-qty 2.5 --fee-bps 10
