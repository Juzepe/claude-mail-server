# =============================================================================
# Makefile — Mail Server Project
# =============================================================================

BINARY      := mailserver-web
BUILD_DIR   := ./web
INSTALL_DIR := /usr/local/bin
SERVICE     := mailserver-web

.PHONY: all build install clean uninstall restart status logs test fmt lint

all: build

## build: Compile the Go web UI binary
build:
	@echo "==> Building $(BINARY)..."
	@cd $(BUILD_DIR) && go build -ldflags="-s -w" -o ../$(BINARY) .
	@echo "    Binary: ./$(BINARY)"

## build-dev: Build with race detector (development)
build-dev:
	@echo "==> Building $(BINARY) (dev)..."
	@cd $(BUILD_DIR) && go build -race -o ../$(BINARY) .

## install: Build and install binary + service
install: build
	@echo "==> Installing $(BINARY) to $(INSTALL_DIR)..."
	@install -m 755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "==> Installing systemd service..."
	@install -m 644 systemd/$(SERVICE).service /etc/systemd/system/
	@systemctl daemon-reload
	@echo "==> Copying project files to /opt/mailserver..."
	@mkdir -p /opt/mailserver
	@cp -r . /opt/mailserver/
	@echo "    Done. Run 'make start' to start the service."

## start: Enable and start the web service
start:
	@systemctl enable --now $(SERVICE)
	@systemctl status $(SERVICE) --no-pager

## restart: Restart the web service
restart:
	@echo "==> Restarting $(SERVICE)..."
	@systemctl restart $(SERVICE)

## stop: Stop the web service
stop:
	@systemctl stop $(SERVICE)

## status: Show service status
status:
	@systemctl status $(SERVICE) --no-pager -l

## logs: Tail service logs
logs:
	@journalctl -u $(SERVICE) -f --no-pager

## uninstall: Remove binary and service
uninstall:
	@echo "==> Stopping and disabling $(SERVICE)..."
	@systemctl stop $(SERVICE) 2>/dev/null || true
	@systemctl disable $(SERVICE) 2>/dev/null || true
	@rm -f /etc/systemd/system/$(SERVICE).service
	@systemctl daemon-reload
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@echo "    Uninstalled. Config and data files were NOT removed."
	@echo "    To fully remove: rm -rf /etc/mailserver /var/lib/mailserver"

## clean: Remove build artifacts
clean:
	@echo "==> Cleaning build artifacts..."
	@rm -f $(BINARY)
	@cd $(BUILD_DIR) && go clean ./...

## fmt: Format Go source code
fmt:
	@echo "==> Formatting Go code..."
	@cd $(BUILD_DIR) && gofmt -w .

## lint: Run go vet
lint:
	@echo "==> Running go vet..."
	@cd $(BUILD_DIR) && go vet ./...

## test: Run Go tests
test:
	@echo "==> Running tests..."
	@cd $(BUILD_DIR) && go test -v ./...

## deps: Download Go module dependencies
deps:
	@echo "==> Downloading dependencies..."
	@cd $(BUILD_DIR) && go mod download && go mod tidy

## cert-renew: Manually renew Let's Encrypt certificate
cert-renew:
	@echo "==> Renewing SSL certificate..."
	@certbot renew --quiet
	@systemctl reload postfix dovecot

## show-users: List all email accounts
show-users:
	@echo "==> Email accounts:"
	@cat /etc/dovecot/users 2>/dev/null | cut -d: -f1 || echo "(no accounts)"

## help: Show this help
help:
	@echo "Mail Server — Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
