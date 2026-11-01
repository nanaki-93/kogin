# Mini-Orca Makefile

# Variables
APP_NAME := mini-orca
VERSION := $(shell sed -n 's/.*Version = "\([^"]*\)"/\1/p' internal/version/version.go)
IMAGE_NAME := $(APP_NAME)
IMAGE_TAG := $(VERSION)
DOCKER_IMAGE := $(IMAGE_NAME):$(IMAGE_TAG)
BUILD_DIR := build
DESKTOP_APP := $(BUILD_DIR)/Mini-Orca.app
# The Applications folder the Finder shows when this user can write there (an
# administrator can), else the one in the home folder.
DESKTOP_INSTALL_DIR ?= $(shell [ -w /Applications ] && echo /Applications || echo "$(HOME)/Applications")
# Info.plist's short version is numbers only: 6.0.0-dev is 6.0.0.
SHORT_VERSION := $(firstword $(subst -, ,$(VERSION)))

# Go settings
GO := go
CGO_ENABLED := 0

# Docker settings
DOCKER := docker
DOCKER_COMPOSE := docker compose

# Colors for output
COLOR_RESET := \033[0m
COLOR_GREEN := \033[32m
COLOR_YELLOW := \033[33m
COLOR_BLUE := \033[34m

# ─── Phony Targets ────────────────────────────────────────────────────────────
.PHONY: all build install build-linux clean test test-race vet fmt-check quality check agent-dispatcher webui-install webui-build webui-test webui-e2e webui-dev webui-dist-check docker-build docker-build-cache docker-run docker-run-detached docker-stop docker-logs docker-restart docker-clean compose-up compose-up-llm compose-down compose-logs compose-restart compose-clean dev dev-watch version help
.PHONY: build-agents build-agents-linux desktop desktop-app install-desktop desktop-check

# ─── Default Target ────────────────────────────────────────────────────────────
all: help

# ─── Help ──────────────────────────────────────────────────────────────────────
help: ## Show this help message
	@echo "$(COLOR_BLUE)Mini-Orca $(VERSION) - Makefile Targets$(COLOR_RESET)"
	@echo ""
	@echo "$(COLOR_GREEN)Build Targets:$(COLOR_RESET)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(COLOR_BLUE)%-20s$(COLOR_RESET) %s\n", $$1, $$2}'
	@echo ""

# ─── Local Build ───────────────────────────────────────────────────────────────
build: build-agents ## Build the mini-orca launcher, the daemon (which serves the Web UI) and the evaluation and inspector commands
	@echo "$(COLOR_GREEN)Building $(APP_NAME) $(VERSION)...$(COLOR_RESET)"
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="-w -s -X github.com/nanaki-93/mini-orca/v2/internal/version.Version=$(VERSION)" -o $(BUILD_DIR)/$(APP_NAME)-daemon ./cmd/daemon
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="-w -s -X github.com/nanaki-93/mini-orca/v2/internal/version.Version=$(VERSION)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/mini-orca
	@cp -f $(BUILD_DIR)/$(APP_NAME) ./$(APP_NAME)
	@cp -f $(BUILD_DIR)/$(APP_NAME)-daemon ./$(APP_NAME)-daemon
	@echo "$(COLOR_GREEN)Build complete: ./$(APP_NAME), ./$(APP_NAME)-daemon ($(BUILD_DIR)/); run mini-orca in a project folder$(COLOR_RESET)"

install: build ## Build and copy mini-orca and mini-orca-daemon into ~/DEV/lab/mini-orca and MINI_ORCA_HOME (or ~/DEV/programs/mini-orca)
	@target_dir="$$HOME/DEV/lab/mini-orca"; \
	if [ -d "$$target_dir" ] && [ "$$(cd "$$target_dir" && pwd)" != "$$(pwd)" ]; then \
		cp -f $(BUILD_DIR)/$(APP_NAME) $(BUILD_DIR)/$(APP_NAME)-daemon "$$target_dir/"; \
	fi
	@if [ -n "$${MINI_ORCA_HOME:-}" ] && [ -d "$${MINI_ORCA_HOME}" ]; then \
		cp -f $(BUILD_DIR)/$(APP_NAME) $(BUILD_DIR)/$(APP_NAME)-daemon "$${MINI_ORCA_HOME}/"; \
		echo "$(COLOR_GREEN)Installed $(APP_NAME) and $(APP_NAME)-daemon into $${MINI_ORCA_HOME}$(COLOR_RESET)"; \
	elif [ -d "$$HOME/DEV/programs/mini-orca" ]; then \
		cp -f $(BUILD_DIR)/$(APP_NAME) $(BUILD_DIR)/$(APP_NAME)-daemon "$$HOME/DEV/programs/mini-orca/"; \
		echo "$(COLOR_GREEN)Installed $(APP_NAME) and $(APP_NAME)-daemon into $$HOME/DEV/programs/mini-orca$(COLOR_RESET)"; \
	fi
	@user_config="$$HOME/.config/mini-orca/config.yaml"; \
	if [ -f config.yaml ] && [ ! -e "$$user_config" ]; then \
		mkdir -p "$$(dirname "$$user_config")" && cp config.yaml "$$user_config" && chmod 600 "$$user_config"; \
		echo "$(COLOR_GREEN)Copied config.yaml to $$user_config$(COLOR_RESET)"; \
	elif [ ! -e "$$user_config" ]; then \
		echo "$(COLOR_YELLOW)No config.yaml: copy config.example.yaml to $$user_config and fill the model scopes$(COLOR_RESET)"; \
	fi
	@echo "$(COLOR_GREEN)Run mini-orca in a project folder to open it in the Web UI$(COLOR_RESET)"

build-linux: build-agents-linux ## Build all Linux amd64 release command binaries
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=amd64 $(GO) build -ldflags="-w -s -X github.com/nanaki-93/mini-orca/v2/internal/version.Version=$(VERSION)" -o $(BUILD_DIR)/$(APP_NAME)-daemon-linux-amd64 ./cmd/daemon
	@echo "$(COLOR_GREEN)Linux build complete: $(BUILD_DIR)/$(APP_NAME)-daemon-linux-amd64$(COLOR_RESET)"

build-agents: ## Build the evaluation and inspector commands without CGO
	@mkdir -p $(BUILD_DIR)
	@for agent_command in engineering-insight-eval agent-eval agent-inspect; do \
		CGO_ENABLED=0 $(GO) build -o $(BUILD_DIR)/$(APP_NAME)-$$agent_command ./cmd/$$agent_command || exit $$?; \
	done

build-agents-linux: ## Cross-build the evaluation and inspector commands for Linux amd64 without CGO
	@mkdir -p $(BUILD_DIR)
	@for agent_command in engineering-insight-eval agent-eval agent-inspect; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(APP_NAME)-$$agent_command-linux-amd64 ./cmd/$$agent_command || exit $$?; \
	done

clean: ## Remove build artifacts
	@echo "$(COLOR_YELLOW)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(BUILD_DIR)
	@echo "$(COLOR_YELLOW)Clean complete$(COLOR_RESET)"

# ─── Desktop App ───────────────────────────────────────────────────────────────
desktop: ## Build the desktop app's program (build/mini-orca-desktop) for this machine; it starts the daemon found beside it or on PATH
	@mkdir -p $(BUILD_DIR)
	@cd desktop && CGO_ENABLED=1 $(GO) build -tags production -trimpath -ldflags="-w -s" -o ../$(BUILD_DIR)/$(APP_NAME)-desktop .
	@echo "$(COLOR_GREEN)Built $(BUILD_DIR)/$(APP_NAME)-desktop$(COLOR_RESET)"

desktop-app: ## Build build/Mini-Orca.app (macOS): the desktop app with its daemon inside
	@[ "$$(uname)" = Darwin ] || { echo "desktop-app builds a macOS app: run it on a Mac (make desktop builds the program elsewhere)"; exit 1; }
	@rm -rf $(DESKTOP_APP)
	@mkdir -p $(DESKTOP_APP)/Contents/MacOS $(DESKTOP_APP)/Contents/Resources
	@cd desktop && CGO_ENABLED=1 $(GO) build -tags production -trimpath -ldflags="-w -s" -o ../$(DESKTOP_APP)/Contents/MacOS/$(APP_NAME)-desktop .
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="-w -s -X github.com/nanaki-93/mini-orca/v2/internal/version.Version=$(VERSION)" -o $(DESKTOP_APP)/Contents/MacOS/$(APP_NAME)-daemon ./cmd/daemon
	@sed -e 's/__SHORT_VERSION__/$(SHORT_VERSION)/' -e 's/__VERSION__/$(VERSION)/' desktop/macos/Info.plist > $(DESKTOP_APP)/Contents/Info.plist
	@cp desktop/macos/icon.icns $(DESKTOP_APP)/Contents/Resources/icon.icns
	@codesign --force --sign - $(DESKTOP_APP)/Contents/MacOS/$(APP_NAME)-daemon
	@codesign --force --sign - $(DESKTOP_APP)
	@echo "$(COLOR_GREEN)Built $(DESKTOP_APP) $(VERSION)$(COLOR_RESET)"

install-desktop: desktop-app ## Build Mini-Orca.app and copy it into /Applications (or ~/Applications; DESKTOP_INSTALL_DIR)
	@mkdir -p "$(DESKTOP_INSTALL_DIR)"
	@rm -rf "$(DESKTOP_INSTALL_DIR)/Mini-Orca.app"
	@ditto $(DESKTOP_APP) "$(DESKTOP_INSTALL_DIR)/Mini-Orca.app"
	@# An earlier install went to ~/Applications; keep a single copy.
	@if [ "$(DESKTOP_INSTALL_DIR)" != "$(HOME)/Applications" ] && [ -d "$(HOME)/Applications/Mini-Orca.app" ]; then \
		rm -rf "$(HOME)/Applications/Mini-Orca.app" && echo "Removed the earlier copy in $(HOME)/Applications"; \
	fi
	@echo "$(COLOR_GREEN)Installed $(DESKTOP_INSTALL_DIR)/Mini-Orca.app$(COLOR_RESET)"
	@echo "Open it from Launchpad or Spotlight, or with: open \"$(DESKTOP_INSTALL_DIR)/Mini-Orca.app\""

desktop-check: ## Vet and cross-build the desktop app for macOS and Linux without cgo (no Xcode or GTK needed)
	@cd desktop && for target in darwin/arm64 darwin/amd64 linux/amd64; do \
		CGO_ENABLED=0 GOOS=$${target%/*} GOARCH=$${target#*/} $(GO) vet -tags purego,production . && \
		CGO_ENABLED=0 GOOS=$${target%/*} GOARCH=$${target#*/} $(GO) build -tags purego,production -o /dev/null . || exit $$?; \
	done

# ─── Testing ───────────────────────────────────────────────────────────────────
test: ## Run Go tests and write the coverage report
	@echo "$(COLOR_GREEN)Running tests...$(COLOR_RESET)"
	@mkdir -p $(BUILD_DIR)
	@$(GO) test ./... -v -coverprofile=$(BUILD_DIR)/coverage.out
	@$(GO) tool cover -html=$(BUILD_DIR)/coverage.out -o $(BUILD_DIR)/coverage.html
	@echo "$(COLOR_GREEN)Tests complete. Coverage report: $(BUILD_DIR)/coverage.html$(COLOR_RESET)"

test-race: ## Run all Go tests under the race detector
	@$(GO) test -race -timeout=25m ./...

vet: ## Vet all Go packages
	@$(GO) vet ./...

agent-dispatcher: ## Run the local dispatcher tests without paid agent calls
	@python3 -m unittest discover -s scripts/tests -p 'test_*.py'

fmt-check: ## Verify Go formatting without modifying files
	@files="$$(gofmt -l $$(find . -path '*/testdata/*' -prune -o -name '*.go' -type f -not -path './build/*' -print))"; test -z "$$files" || { echo "Run go fmt ./...:"; echo "$$files"; exit 1; }

quality: ## Run Go static, reachability, complexity and clone checks
	@./scripts/quality.sh

webui-install: ## Install the Web UI toolchain (web/node_modules) from the lockfile
	@cd web && npm ci

webui-build: ## Type-check and build the Web UI into the embedded internal/api/handlers/webui/dist
	@cd web && npm run build

webui-test: ## Type-check the Web UI and run its unit and component tests
	@if command -v npm >/dev/null 2>&1; then cd web && { test -d node_modules || npm ci; } && npm run typecheck && npm test; else echo "SKIP webui-test: npm is not installed"; fi

webui-e2e: ## Run the Playwright browser tests of the Web UI against a fake daemon
	@cd web && { test -d node_modules || npm ci; } && npm run e2e

webui-dev: ## Serve the Web UI with hot reload on :5173, proxying the API to the daemon on :9090
	@cd web && npm run dev

webui-dist-check: ## Fail when the committed Web UI bundle is out of date with web/src
	@cd web && npm run build >/dev/null && cd .. && git diff --exit-code --stat -- internal/api/handlers/webui/dist && test -z "$$(git ls-files --others --exclude-standard internal/api/handlers/webui/dist)" || { echo "Run make webui-build and commit internal/api/handlers/webui/dist"; exit 1; }

check: fmt-check test test-race vet desktop-check agent-dispatcher webui-test webui-e2e ## Run the complete supported validation path

# ─── Docker Build ──────────────────────────────────────────────────────────────
docker-build: ## Build Docker image
	@echo "$(COLOR_GREEN)Building Docker image $(DOCKER_IMAGE)...$(COLOR_RESET)"
	@$(DOCKER) build \
		--tag $(DOCKER_IMAGE) \
		.
	@echo "$(COLOR_GREEN)Docker image built: $(DOCKER_IMAGE)$(COLOR_RESET)"

docker-build-cache: ## Build Docker image with cache optimization
	@echo "$(COLOR_GREEN)Building Docker image with cache...$(COLOR_RESET)"
	@$(DOCKER) build \
		--cache-from $(IMAGE_NAME):cache \
		--cache-to $(IMAGE_NAME):cache \
		--tag $(DOCKER_IMAGE) \
		.
	@echo "$(COLOR_GREEN)Docker image built with cache: $(DOCKER_IMAGE)$(COLOR_RESET)"

# ─── Docker Run ────────────────────────────────────────────────────────────────
docker-run: ## Run container in detached mode
	@echo "$(COLOR_GREEN)Starting container...$(COLOR_RESET)"
	@$(DOCKER) run -d \
		--name $(APP_NAME) \
		--restart unless-stopped \
		-p 127.0.0.1:9090:9090 \
		-v $$(pwd)/config.yaml:/app/config.yaml:ro \
		-v $$(pwd)/projects:/app/projects:rw \
		-v $$(pwd)/logs:/app/logs:rw \
		-e MINI_ORCA_CONFIG=/app/config.yaml \
		-e MINI_ORCA_BIND_ADDRESS=0.0.0.0:9090 \
		$(DOCKER_IMAGE)
	@echo "$(COLOR_GREEN)Container started: $(APP_NAME)$(COLOR_RESET)"
	@echo "$(COLOR_BLUE)Web UI available at: http://localhost:9090/ui$(COLOR_RESET)"

docker-run-detached: docker-run ## Run container in detached mode (alias)

docker-stop: ## Stop and remove container
	@echo "$(COLOR_YELLOW)Stopping container...$(COLOR_RESET)"
	@$(DOCKER) stop $(APP_NAME) 2>/dev/null || true
	@$(DOCKER) rm $(APP_NAME) 2>/dev/null || true
	@echo "$(COLOR_YELLOW)Container stopped$(COLOR_RESET)"

docker-logs: ## Show container logs
	@$(DOCKER) logs -f $(APP_NAME)

docker-restart: docker-stop docker-run ## Restart container

# ─── Docker Compose ───────────────────────────────────────────────────────────
compose-up: ## Start services with docker-compose
	@echo "$(COLOR_GREEN)Starting services with docker-compose...$(COLOR_RESET)"
	@$(DOCKER_COMPOSE) up -d --build
	@echo "$(COLOR_GREEN)Services started$(COLOR_RESET)"
	@echo "$(COLOR_BLUE)Web UI available at: http://localhost:9090/ui$(COLOR_RESET)"

compose-up-llm: ## Start services with docker-compose including LM Studio
	@echo "$(COLOR_GREEN)Starting services with docker-compose (with LM Studio)...$(COLOR_RESET)"
	@$(DOCKER_COMPOSE) --profile with-llm up -d --build
	@echo "$(COLOR_GREEN)Services started (with LM Studio)$(COLOR_RESET)"
	@echo "$(COLOR_BLUE)Web UI available at: http://localhost:9090/ui$(COLOR_RESET)"

compose-down: ## Stop services with docker-compose
	@echo "$(COLOR_YELLOW)Stopping services...$(COLOR_RESET)"
	@$(DOCKER_COMPOSE) down
	@echo "$(COLOR_YELLOW)Services stopped$(COLOR_RESET)"

compose-logs: ## Show docker-compose logs
	@$(DOCKER_COMPOSE) logs -f

compose-restart: compose-down compose-up ## Restart services

# ─── Docker Clean ──────────────────────────────────────────────────────────────
docker-clean: ## Remove Docker images and containers
	@echo "$(COLOR_YELLOW)Cleaning Docker resources...$(COLOR_RESET)"
	@$(DOCKER) stop $(APP_NAME) 2>/dev/null || true
	@$(DOCKER) rm $(APP_NAME) 2>/dev/null || true
	@$(DOCKER) rmi $(DOCKER_IMAGE) 2>/dev/null || true
	@$(DOCKER) image prune -f
	@echo "$(COLOR_YELLOW)Docker cleanup complete$(COLOR_RESET)"

compose-clean: ## Remove docker-compose resources
	@echo "$(COLOR_YELLOW)Cleaning docker-compose resources...$(COLOR_RESET)"
	@$(DOCKER_COMPOSE) down -v --rmi all --remove-orphans
	@echo "$(COLOR_YELLOW)Docker-compose cleanup complete$(COLOR_RESET)"

# ─── Development ───────────────────────────────────────────────────────────────
dev: ## Run locally for development
	@echo "$(COLOR_GREEN)Running in development mode...$(COLOR_RESET)"
	@$(GO) run ./cmd/daemon

dev-watch: ## Run with air for hot reload (if installed)
	@which air > /dev/null 2>&1 || { echo "Installing air..."; go install github.com/air-verse/air@latest; }
	@air

# ─── Version Info ──────────────────────────────────────────────────────────────
version: ## Show version information
	@echo "$(COLOR_BLUE)Mini-Orca Version: $(VERSION)$(COLOR_RESET)"
	@echo "$(COLOR_BLUE)Go Version: $$(go version)$(COLOR_RESET)"
	@echo "$(COLOR_BLUE)Docker Version: $$(docker --version 2>/dev/null || echo 'not installed')$(COLOR_RESET)"
	@echo "$(COLOR_BLUE)Docker Compose: $$(docker compose version 2>/dev/null || echo 'not installed')$(COLOR_RESET)"
