BIN := bin/tickwatch
VERSION ?= dev
GO ?= go

.PHONY: build test race vet lint cover docker run clean tidy

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/tickwatch

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

docker:
	docker build -t tickwatch:local --build-arg VERSION=$(VERSION) .

run: build
	$(BIN) serve

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin coverage.out dist
