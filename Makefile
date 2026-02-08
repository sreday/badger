BINARY := badger
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

.PHONY: build run clean
