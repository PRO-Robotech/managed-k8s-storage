.PHONY: build test demo vet
build:
	mkdir -p bin
	go build -o bin/ ./cmd/...
test:
	CGO_ENABLED=1 go test -race -count=1 ./...
vet:
	go vet ./...
demo: build
	bash scripts/demo.sh
