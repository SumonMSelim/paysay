# PaySay developer tasks.

BINARY := paysay
PKG := ./...

# AWS profile used by the local/dev run targets. Override on the command line:
#   make run-local AWS_PROFILE=other-profile
AWS_PROFILE ?= sumonmselim
AWS_REGION ?= us-east-1

# PayPal sandbox OAuth endpoint for the `token` target.
PAYPAL_OAUTH_URL ?= https://api-m.sandbox.paypal.com/v1/oauth2/token

.PHONY: all build run run-local run-docker token login test test-race vet fmt tidy cover docker-build docker-up clean help

all: vet test build

build: ## Build the server binary with embedded web assets.
	go build -trimpath -o bin/$(BINARY) ./cmd/paysay

run: ## Run the server locally (reads env / .env via your shell).
	go run ./cmd/paysay

run-local: ## Run with the AWS SSO profile ($(AWS_PROFILE)) wired in. Set PAYPAL_ACCESS_TOKEN in your shell for live PayPal.
	AWS_PROFILE=$(AWS_PROFILE) AWS_REGION=$(AWS_REGION) go run ./cmd/paysay

run-docker: ## Run via docker compose, injecting resolved SSO credentials for the container.
	@echo ">> exporting temporary credentials from profile '$(AWS_PROFILE)' into the compose environment"
	@eval "$$(aws configure export-credentials --profile $(AWS_PROFILE) --format env)"; \
		AWS_REGION=$(AWS_REGION) docker compose up --build

login: ## Refresh the AWS SSO session for the profile (run if credentials expired).
	aws sso login --profile $(AWS_PROFILE)

# Mint a PayPal sandbox access token from client credentials and print an
# export line you can eval. Requires PAYPAL_CLIENT_ID and PAYPAL_SECRET in the
# environment (or your .env), plus `jq`.
#
#   export PAYPAL_CLIENT_ID=...   PAYPAL_SECRET=...
#   eval "$(make -s token)"
#
# The printed token is valid for several hours; re-run when it expires.
token: ## Mint a PayPal sandbox token; usage: eval "$(make -s token)"
	@if [ -z "$$PAYPAL_CLIENT_ID" ] || [ -z "$$PAYPAL_SECRET" ]; then \
		echo "error: set PAYPAL_CLIENT_ID and PAYPAL_SECRET in your environment first" >&2; \
		echo "       (from https://developer.paypal.com -> Apps & Credentials -> Sandbox)" >&2; \
		exit 1; \
	fi
	@TOKEN=$$(curl -s "$(PAYPAL_OAUTH_URL)" \
		-u "$$PAYPAL_CLIENT_ID:$$PAYPAL_SECRET" \
		-d grant_type=client_credentials | jq -r '.access_token // empty'); \
	if [ -z "$$TOKEN" ]; then \
		echo "error: failed to obtain token (check client id/secret and network)" >&2; \
		exit 1; \
	fi; \
	echo "export PAYPAL_ACCESS_TOKEN=$$TOKEN"

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
