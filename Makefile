# PaySay developer tasks.

BINARY := paysay
PKG := ./...

# AWS profile used by the local/dev run targets. Override on the command line:
#   make run-local AWS_PROFILE=other-profile
AWS_PROFILE ?= sumonmselim
AWS_REGION ?= us-east-1

.PHONY: all build run run-local run-docker login test test-race vet fmt tidy cover docker-build docker-up clean help

all: vet test build

build: ## Build the server binary with embedded web assets.
	go build -trimpath -o bin/$(BINARY) ./cmd/paysay

run: ## Run the server locally (reads env / .env via your shell).
	go run ./cmd/paysay

run-local: ## Run with the AWS SSO profile ($(AWS_PROFILE)) wired in. Set PAYPAL_CLIENT_ID/PAYPAL_SECRET in your shell for live PayPal.
	AWS_PROFILE=$(AWS_PROFILE) AWS_REGION=$(AWS_REGION) go run ./cmd/paysay

run-docker: ## Run via docker compose, injecting resolved SSO credentials for the container.
	@echo ">> exporting temporary credentials from profile '$(AWS_PROFILE)' into the compose environment"
	@creds="$$(aws configure export-credentials --profile $(AWS_PROFILE) --format env)" || { \
		echo "error: could not export AWS credentials for '$(AWS_PROFILE)'; run 'make login' and try again" >&2; \
		exit 1; \
	}; \
	eval "$$creds"; \
	AWS_REGION=$(AWS_REGION) docker compose up --build

login: ## Refresh the AWS SSO session for the profile (run if credentials expired).
	aws sso login --profile $(AWS_PROFILE)

help: ## List available targets.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

test: ## Run unit + integration tests.
	go test $(PKG)

test-race: ## Run tests with the race detector.
	go test -race $(PKG)

cover: ## Run tests and open a coverage summary.
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

vet: ## Static analysis.
	go vet $(PKG)

fmt: ## Format all Go code.
	gofmt -s -w .

tidy: ## Tidy module dependencies.
	go mod tidy

docker-build: ## Build the container image.
	docker build -t paysay:latest .

docker-up: ## Run via docker compose.
	docker compose up --build

clean: ## Remove build artifacts.
	rm -rf bin out coverage.out
