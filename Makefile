BINARY := badger
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

run: build
	./$(BINARY)

badger2:
	go build -ldflags "-X main.version=$(VERSION)" -o badger2 ./badger2

scanner:
	go build -ldflags "-X main.version=$(VERSION)" -o snout ./scanner

clean:
	rm -f $(BINARY) badger2 snout

loc:
	@echo "Go:       $$(find . -name '*.go' | xargs wc -l | tail -1 | awk '{print $$1}') lines"
	@echo "Frontend: $$(find . -name '*.html' | xargs wc -l | tail -1 | awk '{print $$1}') lines"

.PHONY: build run badger2 scanner clean loc
