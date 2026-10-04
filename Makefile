.PHONY: build test vet
build:
	go build -trimpath -o hbr ./cmd/hbr
vet:
	go vet ./...
test:
	go test ./...
