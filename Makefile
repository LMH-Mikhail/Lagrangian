APP_NAME := lagrangian
SERVER_BIN := bin/$(APP_NAME)-server
FRONTEND_DIR := frontend
EMBED_DIST := internal/transport/httpapi/webdist/dist

.PHONY: help dev-backend dev-frontend build-frontend build build-linux-amd64 build-linux-arm64 build-windows test clean

help:
	@echo "Lagrangian Makefile targets:"
	@echo "  make test              - Run Go -race tests and frontend typecheck/build"
	@echo "  make build-frontend    - Build Vue 3 frontend and sync assets for Go embed"
	@echo "  make build             - Build frontend + native lagrangian-server single binary"
	@echo "  make build-linux-amd64 - Cross-compile Linux amd64 single binary (zero CGO)"
	@echo "  make build-linux-arm64 - Cross-compile Linux arm64 single binary (zero CGO)"
	@echo "  make build-windows     - Cross-compile Windows amd64 single binary (zero CGO)"
	@echo "  make dev-backend       - Run Go server locally in dev mode"
	@echo "  make dev-frontend      - Run Vite dev server with API proxy"
	@echo "  make clean             - Remove build artifacts"

build-frontend:
	cd $(FRONTEND_DIR) && pnpm install --frozen-lockfile || pnpm install
	cd $(FRONTEND_DIR) && pnpm build
	rm -rf $(EMBED_DIST)
	mkdir -p $(EMBED_DIST)
	touch $(EMBED_DIST)/.gitkeep
	cp -R $(FRONTEND_DIR)/dist/. $(EMBED_DIST)/

build: build-frontend
	mkdir -p bin
	go build -o $(SERVER_BIN) ./cmd/lagrangian-server

build-linux-amd64: build-frontend
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/$(APP_NAME)-server-linux-amd64 ./cmd/lagrangian-server

build-linux-arm64: build-frontend
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/$(APP_NAME)-server-linux-arm64 ./cmd/lagrangian-server

build-windows: build-frontend
	mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/$(APP_NAME)-server-windows-amd64.exe ./cmd/lagrangian-server

dev-backend:
	go run ./cmd/lagrangian-server --dev

dev-frontend:
	cd $(FRONTEND_DIR) && pnpm dev

test:
	go test -v -race ./...
	cd $(FRONTEND_DIR) && pnpm typecheck && pnpm build

clean:
	rm -rf bin dist $(EMBED_DIST)/* $(FRONTEND_DIR)/dist
	touch $(EMBED_DIST)/.gitkeep
