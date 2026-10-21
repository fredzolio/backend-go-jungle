# Jungle Wallet — processamento distribuído de apostas em Go

[![ci](https://github.com/fredzolio/backend-go-jungle/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/fredzolio/backend-go-jungle/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/fredzolio/backend-go-jungle/badge)](https://scorecard.dev/viewer/?uri=github.com/fredzolio/backend-go-jungle)

Serviço em Go 1.27.3 (Uber Fx) que movimenta carteiras a partir de operações de provedores de jogos
(`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), recebidas por HTTP e por SQS FIFO. É um projeto pessoal
que explora como manter saldo e histórico financeiro corretos com várias instâncias, entregas
duplicadas e mensagens fora de ordem. O núcleo é uma carteira com ledger append-only e
idempotência persistente; os eventos saem por uma outbox transacional para SNS FIFO e a
autenticação é OAuth 2.0 / OIDC (client credentials) no Keycloak.

O que o torna interessante do ponto de vista de engenharia:
- **Invariantes no PostgreSQL:** saldo nunca negativo, ledger append-only com cadeia contínua de
  lançamentos, no máximo uma reversão bem-sucedida por transação. Valem por `CHECK`, triggers,
  constraint triggers adiados até o commit e papéis sem `UPDATE`/`DELETE`, independentemente do
  código da aplicação.
- **Idempotência por RFC 8785:** o hash do payload usa JSON canônico (JCS), então a mesma operação
  tem o mesmo hash por HTTP e por SQS. Mesma chave e mesmo payload devolvem o resultado original;
  mesma chave com payload diferente dá `409`. Os vetores de teste vêm de uma implementação
  independente.
- **Resolução fora de ordem:** um `REFUND` ou `ROLLBACK` que chega antes da `BET` de referência fica
  em `PENDING_REFERENCE` e é retomado por qualquer instância, com backoff e prazo, sem lease que
  possa vazar.
- **Outbox com fencing token:** o relay reivindica eventos com `FOR UPDATE SKIP LOCKED`, lease e
  `claim_id`, publica fora de transação SQL e confirma cercado pelo token. Um crash entre publicar e
  confirmar republica o mesmo `eventId`, e a ordem por carteira é preservada.
- **Testes de falha:** a suíte `system` sobe 3+ processos reais do binário (`-race`, `-tags faultinject`)
  e os mata em pontos exatos, como depois do commit e antes do `DeleteMessage` ou entre publicar e
  confirmar na outbox. As suítes de integração usam Postgres e MiniStack reais (testcontainers).

Documentação:
- **Decisões e garantias:** [ARCHITECTURE.md](ARCHITECTURE.md)
- **Contrato HTTP:** [api/openapi.yaml](api/openapi.yaml), também em `/docs`
- **Testes e cenários de negócio:** [docs/TESTING.md](docs/TESTING.md)
- **Carga:** [docs/LOADTEST.md](docs/LOADTEST.md)
- **CI/CD:** [docs/CICD.md](docs/CICD.md) (gates, varreduras de segurança, imagem assinada no GHCR)

Para rodar localmente basta Docker com Compose v2 (`make up`); veja [Subir](#subir).

## Pré-requisitos

- Docker com Compose v2 (é tudo o que precisa para rodar; o Terraform roda em container).
- Para testes: Go 1.27.3 (`GOTOOLCHAIN=go1.27.3` baixa sozinho), Docker; `gcc` para `make test-system` (`-race`).

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
(`/provisioned/aws/producer-provider-a/*`), usando `MessageGroupId = walletId` e o envelope
`WagerTransactionRequested` (`messageId`, `type`, `occurredAt` e `data` com a operação; definido em
`internal/adapters/sqsconsumer/envelope.go`). Exemplo em `test/e2e/sqs_e2e_test.go`.

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

## Roteiro de concorrência e idempotência

Com o stack no ar, existem três clients de demonstração, com o segredo público `jungle-demo-local`
(`DEMO_CLIENT_SECRET` no [.env.example](.env.example); vazio desativa os clients). Eles só valem no
ambiente local e permitem testar sem ler as credenciais geradas pelo Terraform.

| client_id | client_secret | Papel |
|---|---|---|
| `demo-internal` | `jungle-demo-local` | serviço interno: abre e lê carteiras, ledger, reconciliação |
| `demo-provider-1` | `jungle-demo-local` | provider `demo-provider-1`: envia e consulta as próprias transações |
| `demo-provider-2` | `jungle-demo-local` | provider `demo-provider-2`: para testar o isolamento entre providers |

Roteiro completo (bash, `curl`, `jq` e `uuidgen`), incluindo duas apostas simultâneas de 80.00
numa carteira de 100.00:

```bash
BASE=http://localhost:18080
SECRET=jungle-demo-local
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

`WIN`, `LOSS`, `REFUND` e `ROLLBACK` usam o mesmo endpoint (veja `/docs`). O caminho por fila pode
ser testado com `docker compose up --build` e `make test-e2e` (`test/e2e/sqs_e2e_test.go`).

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
infra/terraform/        plano de controle (stack platform)
infra/{keycloak,postgres,traefik,observability}/
test/{e2e,system,load}/
```
