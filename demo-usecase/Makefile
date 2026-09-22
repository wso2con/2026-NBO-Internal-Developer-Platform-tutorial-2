# mopay - local development
#
# Compose before Kubernetes: everything here targets the
# local Docker stack; OpenChoreo manifests live in deploy/ and come later.

SHELL := /bin/bash
COMPOSE := docker compose
GO_SERVICES := ledger collections-api settlement-worker seed loadgen merchant-api

.DEFAULT_GOAL := help
.PHONY: help env build test up down seed reset logs ps verify clean console load token

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

env: ## Generate .env with random local passwords (constraint 6: never committed)
	@if [ -f .env ]; then echo ".env already exists; leaving it alone"; else \
	  sed -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$$(openssl rand -hex 16)|" \
	      -e "s|^APP_DB_PASSWORD=.*|APP_DB_PASSWORD=$$(openssl rand -hex 16)|" \
	      -e "s|^MERCHANT_POSTGRES_PASSWORD=.*|MERCHANT_POSTGRES_PASSWORD=$$(openssl rand -hex 16)|" \
	      -e "s|^MERCHANT_APP_DB_PASSWORD=.*|MERCHANT_APP_DB_PASSWORD=$$(openssl rand -hex 16)|" \
	      .env.example > .env; \
	  echo "wrote .env with generated local passwords"; \
	fi

build: ## Compile and vet every Go service on the host
	@for s in $(GO_SERVICES); do \
	  echo "==> $$s"; \
	  (cd $$s && go build ./... && go vet ./...) || exit 1; \
	done

test: ## Run the money-path tests
	@for s in $(GO_SERVICES); do \
	  echo "==> $$s"; \
	  (cd $$s && go test ./...) || exit 1; \
	done

up: env ## Build images and start the stack
	$(COMPOSE) up -d --build postgres migrate merchant-postgres merchant-migrate merchant-api ledger collections-api settlement-worker
	@echo
	@echo "collections-api  http://localhost:$${COLLECTIONS_PUBLISH_PORT:-8081}/healthz"
	@echo "merchant-api     http://localhost:$${MERCHANT_API_PUBLISH_PORT:-8092}/healthz"
	@echo "ledger           internal only - no published port (hard constraint 8)"

console: ## Start the console as well
	$(COMPOSE) --profile console up -d --build mopay-console
	@echo "console          http://localhost:$${CONSOLE_PUBLISH_PORT:-5173}"

seed: ## Generate the PRD §12 baseline dataset
	$(COMPOSE) --profile tools run --rm --build seed baseline

reset: ## Truncate and reseed from scratch
	$(COMPOSE) --profile tools run --rm --build seed reset

volume: ## Add month-end scale rows: make volume REGION=NG MULT=40
	$(COMPOSE) --profile tools run --rm --build seed volume \
	  --region $${REGION:-NG} --multiplier $${MULT:-40} --period current-month

token: ## Print a dev bearer token: make token ROLE=merchant MERCHANT=mch_001
	@./scripts/token.sh $${ROLE:-operations} $${MERCHANT:-}

load: ## Drive peak-hour intake traffic (Ctrl-C to stop)
	$(COMPOSE) --profile tools run --rm --build loadgen

verify: ## Run the acceptance checks against the running stack
	./scripts/verify.sh

logs: ## Tail every service
	$(COMPOSE) logs -f --tail=50

ps: ## Show stack status
	$(COMPOSE) ps

down: ## Stop the stack, keep data
	$(COMPOSE) --profile tools --profile console down

clean: ## Stop the stack and delete the database volume
	$(COMPOSE) --profile tools --profile console down -v
