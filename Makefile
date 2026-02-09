BINARY := badger
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

loc:
	@echo "Go:       $$(find . -name '*.go' | xargs wc -l | tail -1 | awk '{print $$1}') lines"
	@echo "Frontend: $$(find . -name '*.html' | xargs wc -l | tail -1 | awk '{print $$1}') lines"

.PHONY: build run clean loc
