BINARY   := caravan
PKG      := ./cmd/caravan
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
IMAGE    ?= caravan:$(VERSION)

.PHONY: all build install test vet fmt fmt-check lint tidy run docker check clean

all: build

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

install:
	CGO_ENABLED=0 go install -ldflags "$(LDFLAGS)" $(PKG)

test:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	@out=$$(gofmt -l -s .); if [ -n "$$out" ]; then echo "Unformatted files:"; echo "$$out"; exit 1; fi

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

run: build
	./bin/$(BINARY)

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# Mirror the CI checks locally.
check: fmt-check vet lint test

clean:
	rm -rf bin dist coverage.out
