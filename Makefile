VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST := internal/webui/dist
IMAGE ?= webdav-nvr-player:dev
# web/node_modules can contain stray Go packages, so never use ./...
GOPKGS := ./cmd/... ./internal/...

.PHONY: all web build test lint e2e run dev reset-password docker clean

all: web build

## web: build the frontend into internal/webui/dist (embedded by the Go binary)
web: web/node_modules
	find $(DIST) -mindepth 1 ! -name README.md -exec rm -rf {} +
	cd web && npm run build

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	@touch $@

## build: compile bin/nvr with whatever UI is currently in internal/webui/dist
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/nvr ./cmd/nvr

## test: Go tests (race detector) plus frontend typecheck
test: web/node_modules
	go test -race $(GOPKGS)
	cd web && npm run typecheck

## lint: go vet, gofmt check, eslint
lint: web/node_modules
	go vet $(GOPKGS)
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	cd web && npm run lint

## e2e: build the Docker image and run the Playwright tests against it (e2e/run.sh)
e2e:
	NVR_IMAGE=$(IMAGE) ./e2e/run.sh

## run: build the UI and server, then start it with the settings in .env
run: .env all
	@set -a && . ./.env && set +a && exec ./bin/nvr

## reset-password: print a new password for the admin user (signs it out everywhere)
reset-password: .env build
	@set -a && . ./.env && set +a && exec ./bin/nvr reset-password

## dev: run the Go server with .env; start `npm run dev` in web/ separately for hot reload
dev: .env
	@set -a && . ./.env && set +a && LOG_LEVEL=debug exec go run ./cmd/nvr

# First run: create .env from the template and stop so it can be filled in.
.env:
	@cp .env.example .env
	@echo ""
	@echo "Created .env from .env.example."
	@echo "Fill in WEBDAV_URL, WEBDAV_USERNAME and WEBDAV_PASSWORD, then run 'make run' again."
	@echo ""
	@exit 1

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean:
	rm -rf bin data
	find $(DIST) -mindepth 1 ! -name README.md -exec rm -rf {} +
