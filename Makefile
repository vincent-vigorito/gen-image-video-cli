BIN := bin/giv

build:
	go build -o $(BIN) ./cmd/giv

.PHONY: build
