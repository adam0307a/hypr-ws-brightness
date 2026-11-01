BINARY_NAME := hypr-ws-brightness
VERSION     ?= 0.1.0
PREFIX      ?= $(HOME)/.local
BINDIR      := $(PREFIX)/bin
GO          ?= go
LDFLAGS     := -s -w -X main.version=$(VERSION)

.PHONY: all build release test vet fmt install uninstall clean

all: build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/hypr-ws-brightness

release: clean
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY_NAME) ./cmd/hypr-ws-brightness
	tar -czf dist/$(BINARY_NAME)-$(VERSION)-linux-amd64.tar.gz -C dist $(BINARY_NAME) -C .. LICENSE README.md config.example.toml
	rm -f dist/$(BINARY_NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY_NAME) ./cmd/hypr-ws-brightness
	tar -czf dist/$(BINARY_NAME)-$(VERSION)-linux-arm64.tar.gz -C dist $(BINARY_NAME) -C .. LICENSE README.md config.example.toml
	rm -f dist/$(BINARY_NAME)
	cd dist && sha256sum *.tar.gz > checksums.txt
	@echo "Release archives created in dist/"

test:
	$(GO) test -v -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

install: build
	install -Dm755 $(BINARY_NAME) $(DESTDIR)$(BINDIR)/$(BINARY_NAME)
	@echo "Installed $(BINARY_NAME) to $(DESTDIR)$(BINDIR)/$(BINARY_NAME)"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BINARY_NAME)
	@echo "Removed $(DESTDIR)$(BINDIR)/$(BINARY_NAME)"

clean:
	rm -rf $(BINARY_NAME) dist
