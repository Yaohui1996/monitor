APP     := monitor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST    := dist

.PHONY: all build run test race vet fmt tidy cross clean

all: build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(APP) .

run: build
	./$(APP) -open

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

cross:
	@mkdir -p $(DIST)
	@for os in darwin linux windows; do \
		for arch in amd64 arm64; do \
			ext=""; \
			if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
			echo "build $(DIST)/$(APP)-$$os-$$arch$$ext"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(APP)-$$os-$$arch$$ext . || exit 1; \
		done; \
	done
	@cd $(DIST) && (shasum -a 256 $(APP)-* > SHA256SUMS 2>/dev/null || sha256sum $(APP)-* > SHA256SUMS)
	@echo "done: $(DIST)/"

clean:
	rm -rf $(DIST) $(APP)
