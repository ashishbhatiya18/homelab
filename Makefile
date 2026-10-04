.PHONY: build test vet
build:
	go build -trimpath -o home ./cmd/home
vet:
	go vet ./...
test:
	go test ./...
