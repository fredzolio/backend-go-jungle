# Jungle wallet — developer entry points. Everything runs in the isolated compose project "jungle".
SHELL := /bin/bash
# The VM lab uses .env.lab (created by `make lab-init`); a plain checkout uses defaults/.env.
ENV_FILE ?= $(if $(wildcard .env.lab),.env.lab,)
COMPOSE ?= docker compose $(if $(ENV_FILE),--env-file $(ENV_FILE),)
export GOTOOLCHAIN ?= go1.27.1

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------- environment lifecycle ----------
.PHONY: up down destroy ps logs provision outputs migrate-status migrate-down migrate-up obs-up
up: ## Build and start the whole environment (detached)
	$(COMPOSE) up --build -d --wait

down: ## Stop the environment, keeping data volumes
	$(COMPOSE) --profile obs down

destroy: ## Stop and delete EVERYTHING of this environment (public route, containers, volumes, local images)
	-@$(MAKE) --no-print-directory lab-unexpose 2>/dev/null
	$(COMPOSE) --profile obs --profile edge down -v --rmi local --remove-orphans

obs-up: ## Start Prometheus (127.0.0.1:19090) and Grafana (127.0.0.1:13000)
	$(COMPOSE) --profile obs up -d prometheus grafana

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

# ---------- VM lab (https://jungle.lab.fredzol.io) ----------
.PHONY: lab-init lab-expose lab-unexpose lab-smoke
lab-init: ## Create .env.lab with the public URL and strong random bootstrap secrets (once)
	@test -f .env.lab && echo ".env.lab already exists" || { \
	  umask 077; r() { openssl rand -hex 24; }; { \
	  echo "PUBLIC_BASE_URL=https://jungle.lab.fredzol.io"; echo "ADMIN_BASE_URL=http://localhost:18090"; \
	  echo "POSTGRES_PASSWORD=$$(r)"; echo "KEYCLOAK_DB_PASSWORD=$$(r)"; echo "KEYCLOAK_ADMIN_PASSWORD=$$(r)"; \
	  echo "MINISTACK_ROOT_SECRET=$$(r)"; echo "GRAFANA_ADMIN_PASSWORD=$$(r)"; echo "LOG_LEVEL=info"; } > .env.lab; \
	  echo ".env.lab created (bootstrap secrets apply to fresh volumes: run make destroy && make up)"; }

# The VM Caddy 2.6.2 panics on admin-API reloads (also `systemctl reload`), so the
# route is applied/validated by Terraform and Caddy is *restarted* (brief blip for
# every site on the VM) only after a successful apply/destroy.
lab-expose: ## Publish https://jungle.lab.fredzol.io on the VM Caddy (Terraform stack edge-lab)
	$(COMPOSE) --profile edge run --rm --build edge-lab apply
	sudo systemctl restart caddy

lab-unexpose: ## Remove the public route (Terraform destroy of edge-lab)
	$(COMPOSE) --profile edge run --rm --build edge-lab destroy
	sudo systemctl restart caddy

lab-smoke: ## Run the e2e suite through the public HTTPS URL
	@$(MAKE) --no-print-directory test-e2e E2E_BASE_URL=https://jungle.lab.fredzol.io

# ---------- Go ----------
.PHONY: build fmt vet test test-race test-integration test-e2e test-system
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

test-system: ## Multi-process + crash scenarios: real binary (-race, faultinject) as independent processes
	go test -tags=system -count=1 -timeout=15m -v ./test/system/...

test-e2e: ## End-to-end tests against the running stack (make up first): real Keycloak tokens, real SQS
	@tmp=$$(mktemp -d -p $(CURDIR) .e2e-XXXXXX) && trap 'rm -rf $$tmp' EXIT && \
	docker run --rm -v jungle_provisioned:/p:ro -v $$tmp:/out alpine sh -c 'cp -r /p/. /out/ && chown -R $(shell id -u):$(shell id -g) /out' && \
	JUNGLE_BASE_URL=$(E2E_BASE_URL) JUNGLE_PROVISIONED_DIR=$$tmp go test -tags=e2e -count=1 -v ./test/e2e/...

# ---------- Terraform ----------
TF_IMAGE := hashicorp/terraform:1.16.4
TF_DOCKER := docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/infra/terraform:/w -w /w

.PHONY: tf-fmt tf-validate
tf-fmt: ## Format Terraform files
	$(TF_DOCKER) $(TF_IMAGE) fmt -recursive .

tf-validate: ## Validate the platform stack
	$(TF_DOCKER) --entrypoint sh -w /w/stacks/platform $(TF_IMAGE) -c 'terraform init -backend=false -lockfile=readonly >/dev/null && terraform validate; rm -rf .terraform'
