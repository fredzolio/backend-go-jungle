# Jungle wallet — developer entry points. Everything runs in the isolated compose project "jungle".
SHELL := /bin/bash
COMPOSE ?= docker compose
export GOTOOLCHAIN ?= go1.27.1

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------- environment lifecycle ----------
.PHONY: up down destroy ps logs provision outputs migrate-status migrate-down migrate-up
up: ## Build and start the whole environment (detached)
	$(COMPOSE) up --build -d --wait

down: ## Stop the environment, keeping data volumes
	$(COMPOSE) down

destroy: ## Stop and delete EVERYTHING of this environment (containers, volumes, local images)
	$(COMPOSE) down -v --rmi local --remove-orphans

ps: ## Service status
	$(COMPOSE) ps

logs: ## Follow logs (S=service to filter)
	$(COMPOSE) logs -f --tail=100 $(S)

provision: ## Re-run Terraform provisioning (idempotent)
	$(COMPOSE) run --rm provisioner

outputs: ## Show non-sensitive Terraform outputs
	$(COMPOSE) run --rm provisioner output

migrate-status: ## Show applied/pending migrations
	$(COMPOSE) run --rm migrate migrate status

migrate-up: ## Apply pending migrations
	$(COMPOSE) run --rm migrate migrate up

migrate-down: ## Revert the latest migration
	$(COMPOSE) run --rm migrate migrate down

# ---------- Go ----------
.PHONY: build fmt vet test test-race test-integration
build: ## Compile all packages
	go build ./...

fmt: ## Format Go code
	gofmt -w cmd internal

vet: ## go vet
	go vet ./...

test: ## Unit tests
	go test -count=1 ./...

test-race: ## Unit tests with the race detector
	go test -race -shuffle=on -count=1 ./...

test-integration: ## Integration tests against real containers (Docker required)
	go test -tags=integration -race -count=1 ./...

# ---------- Terraform ----------
TF_IMAGE := hashicorp/terraform:1.16.4
TF_DOCKER := docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/infra/terraform:/w -w /w

.PHONY: tf-fmt tf-validate
tf-fmt: ## Format Terraform files
	$(TF_DOCKER) $(TF_IMAGE) fmt -recursive .

tf-validate: ## Validate the platform stack
	$(TF_DOCKER) --entrypoint sh -w /w/stacks/platform $(TF_IMAGE) -c 'terraform init -backend=false -lockfile=readonly >/dev/null && terraform validate; rm -rf .terraform'
