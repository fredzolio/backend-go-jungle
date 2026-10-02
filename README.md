# Jungle Wallet — processamento distribuído de apostas em Go

[![ci](https://github.com/fredzolio/backend-go-jungle/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/fredzolio/backend-go-jungle/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/fredzolio/backend-go-jungle/badge)](https://scorecard.dev/viewer/?uri=github.com/fredzolio/backend-go-jungle)

Solução do [desafio backend da Jungle Gaming](docs/CHALLENGE.md). É um serviço em Go 1.27.1 com
Uber Fx que movimenta carteiras a partir de operações de provedores, recebidas por HTTP e por SQS
FIFO. Tem:
- idempotência persistente e ledger append-only;
- invariantes garantidas pelo PostgreSQL;
- inbox e outbox transacionais (SNS FIFO);
- OAuth 2.0 / OIDC com Keycloak.

- **Decisões e garantias:** [ARCHITECTURE.md](ARCHITECTURE.md)
- **Contrato HTTP:** [api/openapi.yaml](api/openapi.yaml), também em `/docs`
- **Testes e cenários obrigatórios:** [docs/TESTING.md](docs/TESTING.md)
- **Carga:** [docs/LOADTEST.md](docs/LOADTEST.md)
- **CI/CD:** [docs/CICD.md](docs/CICD.md) (gates, imagem assinada, deploy no lab via Tailscale)
- **Diário de implementação:** [CHECKPOINT.md](CHECKPOINT.md)
- **Testar no ambiente público:** [seção abaixo](#testar-no-ambiente-público) (credenciais de avaliação incluídas)

## Pré-requisitos

- Docker com Compose v2 (é tudo o que precisa para rodar; o Terraform roda em container).
- Para testes: Go 1.27.1 (`GOTOOLCHAIN=go1.27.1` baixa sozinho), Docker; `gcc` para `make test-system` (`-race`).

## Subir

```bash
docker compose up --build -d --wait     # ou: make up
curl -s http://localhost:18080/health/ready
```

O `up` sobe o ambiente em ordem:
1. Postgres, Keycloak, MiniStack;
2. **provisioner** (Terraform): realm e clients, filas e tópico com IAM, papéis do banco;
3. **migrate** (goose, como dono do schema);
4. `api-1..3` atrás do Traefik.

| Endereço (127.0.0.1) | O quê |
|---|---|
| `:18080/` | API (edge, balanceado entre 3 instâncias) · `/docs` · `/openapi.yaml` |
| `:18080/auth/realms/jungle/protocol/openid-connect/token` | emissão de tokens (client credentials) |
| `:18090/auth/admin` | console do Keycloak (entrada privada; usuário `admin`) |
| `:15432` | Postgres (debug) · `:14566` MiniStack (SQS/SNS/IAM) |
| `:19090` / `:13000` | Prometheus / Grafana (`make obs-up`) |

Desligar: `make down` (mantém dados). Apagar tudo: `make destroy`. Se o MiniStack cair (estado em
memória): `make recover-broker`.

## Variáveis de ambiente

Todas têm padrão no `compose.yaml`; [.env.example](.env.example) lista as do ambiente. Os segredos
dos serviços (senhas dos papéis, secrets dos clients, chaves IAM, ARN do tópico) são **gerados pelo
Terraform** e entregues como arquivos (volume `jungle_provisioned`), lidos via variáveis `*_FILE`.

| Variável | Para quê |
|---|---|
| `PUBLIC_BASE_URL` | URL pública do edge; define o `iss` dos tokens (`<url>/auth/realms/jungle`) |
| `POSTGRES_PASSWORD`, `KEYCLOAK_DB_PASSWORD`, `KEYCLOAK_ADMIN_PASSWORD`, `MINISTACK_ROOT_SECRET` | segredos de bootstrap (antes do Terraform) |
| `ROLES` (por instância) | `resolver,consumer,outbox[,reconciler]` |
| `DB_*`, `AWS_*`, `OIDC_*`, `CONSUMER_*`, `OUTBOX_*`, `REFERENCE_*` | ver `internal/platform/config/config.go` (com padrões documentados) |

## Filas, tópico e IdP

Tudo é criado pelo Terraform (`infra/terraform/stacks/platform`) a cada `up`, de forma idempotente:
- `wager-transactions.fifo` + `wager-transactions-dlq.fifo` (redrive após 5 entregas);
- SNS `wager-events.fifo` → `wager-events-audit.fifo` (+ DLQ);
- um principal IAM por componente e por provider;
- realm `jungle` com os clients `provider-a`, `provider-b`, `jungle-internal` e `provider-c-shortlived`.

Para reaplicar: `make provision`. Saídas: `make outputs`.

## Migrations

```bash
make migrate-status    # estado
make migrate-up        # aplica pendentes
make migrate-down      # reverte a última
docker compose run --rm migrate migrate reset   # reverte todas
```

## Exemplos de chamadas

```bash
CLIENTS=$(docker run --rm -v jungle_provisioned:/p:ro alpine cat /p/keycloak/clients.json)
secret() { echo "$CLIENTS" | jq -r ".\"$1\".client_secret"; }
token()  { curl -s -X POST http://localhost:18080/auth/realms/jungle/protocol/openid-connect/token \
             -d grant_type=client_credentials -d client_id=$1 -d client_secret=$(secret $1) | jq -r .access_token; }
INTERNAL=$(token jungle-internal); PROVIDER=$(token provider-a)

# abrir carteira (interno)
WALLET=$(curl -s -X POST http://localhost:18080/wallets -H "Authorization: Bearer $INTERNAL" \
  -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}')
WID=$(echo "$WALLET" | jq -r .id)

# aposta (provider); repetir a mesma chamada devolve idempotentReplay=true e o mesmo saldo
curl -s -X POST http://localhost:18080/wagering/transactions -H "Authorization: Bearer $PROVIDER" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: provider-a:transaction-123' \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"transaction-123\",
       \"playerId\":\"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1\",\"walletId\":\"$WID\",\"roundId\":\"round-987\",
       \"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"

# leituras e reconciliação
curl -s http://localhost:18080/wallets/$WID -H "Authorization: Bearer $INTERNAL"
curl -s "http://localhost:18080/wallets/$WID/ledger?limit=50" -H "Authorization: Bearer $INTERNAL"
curl -s http://localhost:18080/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $PROVIDER"
curl -s -X POST http://localhost:18080/wallets/$WID/reconciliation -H "Authorization: Bearer $INTERNAL"
```

Pela fila, um provider publica no `wager-transactions.fifo` com suas credenciais IAM
(`/provisioned/aws/producer-provider-a/*`), usando `MessageGroupId = walletId` e o envelope do
desafio. Exemplo em `test/e2e/sqs_e2e_test.go`.

## Testes

```bash
go test ./...                  # unitários (ou make test)
go test -race ./...            # (make test-race)
go vet ./...
make test-integration          # Postgres/MiniStack reais via testcontainers (tag integration)
make test-system               # 3+ processos reais com -race e pontos de falha (tag system)
make up && make test-e2e       # stack completo, Keycloak real (tag e2e)
make load-test                 # k6
```

Detalhes, matriz de cenários e simulação de falhas: [docs/TESTING.md](docs/TESTING.md).

## Testar no ambiente público

`https://jungle.lab.fredzol.io` roda a versão atual da `main`, com 3 instâncias da API atrás do edge.
O contrato fica em [`/docs`](https://jungle.lab.fredzol.io/docs). Para autenticar, use os clients de
avaliação abaixo. O segredo é público de propósito: estes clients existem só para testar o lab.

| client_id | client_secret | Papel |
|---|---|---|
| `demo-internal` | `jungle-lab-demo-2026` | serviço interno: abre e lê carteiras, ledger, reconciliação |
| `demo-provider-1` | `jungle-lab-demo-2026` | provider `demo-provider-1`: envia e consulta as próprias transações |
| `demo-provider-2` | `jungle-lab-demo-2026` | provider `demo-provider-2`: para testar o isolamento entre providers |

Roteiro completo (bash, `curl`, `jq` e `uuidgen`), incluindo o teste obrigatório de duas apostas
simultâneas de 80.00 numa carteira de 100.00:

```bash
BASE=https://jungle.lab.fredzol.io
SECRET=jungle-lab-demo-2026
token() { curl -s -X POST "$BASE/auth/realms/jungle/protocol/openid-connect/token" \
  -d grant_type=client_credentials -d client_id="$1" -d client_secret="$SECRET" | jq -r .access_token; }
INTERNAL=$(token demo-internal); P1=$(token demo-provider-1); P2=$(token demo-provider-2)
RUN=$(date +%s); PLAYER=$(uuidgen | tr 'A-Z' 'a-z'); T=$(mktemp -d)   # IDs novos a cada execução

# carteira com 100.00 BRL (cliente interno)
WID=$(curl -s -X POST "$BASE/wallets" -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}" | jq -r .id)

bet() { # <token> <providerId> <externalTransactionId> <amount>
  curl -s -X POST "$BASE/wagering/transactions" -H "Authorization: Bearer $1" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $2:$3" \
    -d "{\"providerId\":\"$2\",\"externalTransactionId\":\"$3\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WID\",
         \"roundId\":\"round-$RUN\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"$4\",\"currency\":\"BRL\"}}"
}

# duas apostas de 80.00 ao mesmo tempo -> uma PROCESSED, outra REJECTED/INSUFFICIENT_FUNDS
bet "$P1" demo-provider-1 "bet-a-$RUN" 80.00 > "$T/a" & bet "$P1" demo-provider-1 "bet-b-$RUN" 80.00 > "$T/b" & wait
jq -c . "$T/a" "$T/b"

# reenvio: mesma resposta, idempotentReplay=true, saldo inalterado
bet "$P1" demo-provider-1 "bet-a-$RUN" 80.00 | jq -c .

# saldo 20.00, ledger com o crédito inicial e um único débito, reconciliação consistente
curl -s "$BASE/wallets/$WID" -H "Authorization: Bearer $INTERNAL" | jq -c .
curl -s "$BASE/wallets/$WID/ledger?limit=50" -H "Authorization: Bearer $INTERNAL" | jq -c '.items[] | {direction, money, balanceAfter}'
curl -s -X POST "$BASE/wallets/$WID/reconciliation" -H "Authorization: Bearer $INTERNAL" | jq -c .

# autorização: 404 (provider não enxerga transação de outro), 403 PROVIDER_MISMATCH, 403 (escopo), 401
curl -s -o /dev/null -w '%{http_code}\n' "$BASE/providers/demo-provider-1/wagering/transactions/bet-a-$RUN" -H "Authorization: Bearer $P2"
bet "$P2" demo-provider-1 "x-$RUN" 1.00 | jq -r .code
curl -s -o /dev/null -w '%{http_code}\n' "$BASE/wallets/$WID" -H "Authorization: Bearer $P1"
curl -s -o /dev/null -w '%{http_code}\n' "$BASE/wallets/$WID"
rm -rf "$T"
```

`WIN`, `LOSS`, `REFUND` e `ROLLBACK` usam o mesmo endpoint (veja `/docs`). O lab expõe só HTTP:
a fila SQS (MiniStack), o console do Keycloak e as métricas ficam privados. O caminho por fila
pode ser testado localmente com `docker compose up --build` e `make test-e2e`
(`test/e2e/sqs_e2e_test.go`). Localmente existem os mesmos clients de avaliação, com o segredo
`jungle-demo-local` (`DEMO_CLIENT_SECRET` no `.env.example`; vazio desativa os clients).

## Ambiente público (VM do autor)

`https://jungle.lab.fredzol.io` aponta para este mesmo stack. A rota é gerida pelo stack Terraform
`edge-lab`:

```bash
make lab-init      # .env.lab com a URL pública e segredos fortes (uma vez)
make destroy && make up
make lab-expose    # publica no Caddy da VM (make lab-unexpose remove)
make lab-smoke     # suíte e2e pelo HTTPS público
```

Só ficam públicos a API, `/docs` e os endpoints de token, JWKS e discovery do realm `jungle`.

Depois do setup inicial, quem atualiza o lab é o pipeline: cada push verde na `main` faz rolling
deploy com smoke e rollback automático ([docs/CICD.md](docs/CICD.md)). O checkout usado pelo deploy
é `/home/zolio/deploy/backend-go-jungle`, com o env decifrado do SOPS (`deploy/lab/lab.enc.env`).

## Estrutura

```
cmd/jungle/             binário único: serve | migrate | healthcheck
internal/domain/        Money, Wallet, WagerTransaction, regras, eventos (puro)
internal/app/           casos de uso e ports
internal/adapters/      postgres, httpapi, oidc, sqsconsumer, snspublisher, awsx
internal/workers/       loops de background no ciclo de vida do Fx
internal/bootstrap/     composição Fx (roles)
migrations/             SQL versionado (goose)
api/openapi.yaml        contrato HTTP
infra/terraform/        plano de controle (platform) e rota pública (edge-lab)
infra/{keycloak,postgres,traefik,observability}/
test/{e2e,system,load}/
```
