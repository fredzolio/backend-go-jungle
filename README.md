# Jungle Wallet — Distributed wager processing in Go

Solution for the [Jungle Gaming backend challenge](docs/CHALLENGE.md): a Go + Uber Fx service that
moves player wallets from game-provider operations (HTTP and SQS), with a persistent idempotent,
append-only ledger, transactional inbox/outbox and OAuth2/OIDC (Keycloak).

> 🚧 Work in progress. Implementation status lives in [CHECKPOINT.md](CHECKPOINT.md).
> Full documentation (architecture, API, tests) arrives with phase F11.

## Quick start

Requirements: Docker with Compose v2. Nothing else (Terraform runs inside a container).

```bash
docker compose up --build -d --wait   # or: make up
curl -s http://localhost:18080/health/ready
```

| URL | What |
|---|---|
| `http://localhost:18080/` | API (behind the Traefik edge, load-balanced over `api-1..3`) |
| `http://localhost:18080/auth/realms/jungle` | Keycloak realm (token endpoint, JWKS) |
| `http://localhost:18090/auth/admin` | Keycloak admin console (private entrypoint; user `admin`) |

Stop with `make down` (keeps data) or remove everything with `make destroy`.

## Layout

```
cmd/jungle/            single binary (serve | healthcheck)
internal/              application code (domain, app, adapters, platform, bootstrap)
infra/terraform/       control plane: Keycloak realm, SQS/SNS/IAM (MiniStack), Postgres roles
infra/{keycloak,postgres,traefik}/  container configuration
compose.yaml           isolated environment (project "jungle")
```
