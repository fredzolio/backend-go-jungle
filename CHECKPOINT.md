# CHECKPOINT — Jungle Wallet (backend-challenge-go)

> Documento vivo de acompanhamento da implementação, do início ao fim.
> **Regra:** toda sessão de trabalho começa lendo este arquivo e termina atualizando-o
> (status das fases, log de progresso, decisões novas, pendências).

- Desafio: <https://github.com/junglegaming/backend-challenge-go> (spec copiada em `docs/CHALLENGE.md`)
- Repositório: `github.com/fredzolio/backend-go-jungle` (privado, **não** é fork)
- Ambiente público (lab): `https://jungle.lab.fredzol.io` (Fase 10)

---

## 1. Como retomar o trabalho

1. Ler este arquivo inteiro (principalmente §4 Fases e §7 Log).
2. `git log --oneline -15` para ver onde parou.
3. `make up` sobe o ambiente; `make ps` confere saúde; `make down` desliga.
4. Continuar a primeira caixa `[ ]` da fase marcada como **EM ANDAMENTO**.
5. Ao terminar um bloco: marcar caixas, registrar no §7 e commitar.

## 2. Ambiente isolado na VM (liga/desliga)

Tudo pertence ao projeto Compose **`jungle`** e não toca em nada fora dele.

| Recurso | Nome / valor |
|---|---|
| Projeto Compose | `jungle` (fixo em `compose.yaml` → `name: jungle`) |
| Containers | `jungle-*` |
| Rede | `jungle_net` (bridge própria) |
| Volumes | `jungle_pgdata`, `jungle_tfstate`, `jungle_provisioned`, `jungle_ministack` |
| Imagens locais | `jungle/*` |
| Portas no host | **somente `127.0.0.1`**: `18080` (entrada pública/edge), `18090` (admin), `15432` (Postgres p/ debug), `14566` (MiniStack p/ testes) |
| Exposição pública | 1 rota no Caddy do host: `jungle.lab.fredzol.io → 127.0.0.1:18080` (Fase 10, via Terraform `edge-lab`) |

| Ação | Comando |
|---|---|
| Ligar | `make up` |
| Desligar (mantém dados) | `make down` |
| Desligar e apagar **tudo** (containers, volumes, imagens locais, rota pública) | `make destroy` |
| Status / logs | `make ps` / `make logs` |

## 3. Decisões assumidas (registro)

| # | Decisão | Motivo |
|---|---|---|
| D1 | **Compose = runtime; Terraform (em container) = plano de controle**: realm/clients Keycloak, filas/tópico/IAM no MiniStack, roles do Postgres e (lab) rota no Caddy | Compose é exigido pelo desafio; evita duplicar definição de containers |
| D2 | Terraform roda no serviço `provisioner` do próprio compose → `docker compose up --build` provisiona tudo sem Terraform no host | Reprodutível a partir de checkout limpo |
| D3 | **MiniStack** (`ministackorg/ministack:1.5.19`) com `AUTH=true`; plano B: LocalStack 4.9.2 fixado | LocalStack exige auth token desde 2026-03-23; MiniStack ~30 MB |
| D4 | **Um único hostname**: API em `/`, Keycloak em `/auth` (`KC_HTTP_RELATIVE_PATH=/auth`), docs em `/docs` | Uma rota no Caddy, um certificado, issuer estável |
| D5 | Público: API + endpoints do realm + docs. Privado (127.0.0.1/túnel SSH/Tailscale): console admin do Keycloak, dashboard Traefik, métricas, Grafana | Superfície mínima |
| D6 | Edge do stack = **Traefik v3** (LB entre `api-1..3`, health check, rate limit), sem docker socket | Seguro e suficiente |
| D7 | Go **1.27.1**; pgx/v5 + SQL explícito; goose (migrations); `net/http` ServeMux; Uber Fx; slog | Alinhado ao desafio e aos melhores forks |
| D8 | Dinheiro em `int64` (centavos) + `CHAR(3)`; parser estrito `^(0\|[1-9]\d*)\.\d{2}$` | Sem normalização antes do hash |
| D9 | Credenciais demo dos providers **não** publicadas; entregues por fora | Segurança do ambiente público |
| D10 | Autorização de remetente no SQS: policy do broker (só `jungle-producer-<id>` envia) + validação de domínio no consumer + mapa `senders.json` (SenderId→provider). No MiniStack o `SenderId` é o account id, então o mapa aceita o account como curinga (documentado como limitação; em AWS real usa o `AIDA…` do usuário) | Resultado do spike F0 |
| D11 | Secrets gerados pelo Terraform (`random_password`) e entregues em volume `jungle_provisioned` (arquivos 0400, uid 65532), lidos via variáveis `*_FILE` | Nada de segredo em env/git |

Pinagem de versões: Postgres `17.11-alpine` · Keycloak `26.8.0` · MiniStack `1.5.19` · Terraform `1.16.4`
(providers: aws `6.67.0`, keycloak `5.9.0`, postgresql `1.27.0`) · Traefik `v3.7.13` · Go `1.27.1-alpine3.24` · runtime `distroless/static-debian12:nonroot`.

## 4. Fases

Legenda: `[ ]` pendente · `[x]` feito · `[~]` parcial · **status** da fase no título.

### F0 — Fundação e ambiente isolado — **CONCLUÍDA** (2026-10-01)
- [x] Repo git + remoto privado + `.gitignore`/`.dockerignore`/`.editorconfig`
- [x] Spec do desafio em `docs/CHALLENGE.md`
- [x] `go.mod` (Go 1.27.1) + esqueleto `cmd/jungle` (Fx, config, slog, health live/ready, shutdown com drenagem)
- [x] `Dockerfile` multi-stage distroless non-root + subcomando `healthcheck`
- [x] `compose.yaml`: postgres, keycloak, ministack, provisioner, api-1..3, edge (read-only, no-new-privileges, mem_limit)
- [x] Imagem Keycloak otimizada (`kc.sh build`) com `/auth`
- [x] Terraform `stacks/platform` + módulos `postgres_access`, `keycloak_realm`, `messaging` (78 recursos; lock file versionado)
- [x] `Makefile` (up/down/destroy/ps/logs/provision/outputs/test/tf-*) e `.env.example`
- [x] Spike: MiniStack `AUTH=true` aplica policies IAM em SQS/SNS → **sim** (ver §6)
- [x] Spike: `SenderId` no ReceiveMessage → **retorna o account id, não o usuário** (ver §6, D10)
- [x] Spike: redrive FIFO para DLQ após `maxReceiveCount` → **funciona**
- [x] Spike: `iss` estável por fora e por dentro → **sim**
- [x] `docker compose up --build` verde de ponta a ponta; ciclo `down`/`up` reprovisiona com "No changes"

### F1 — Domínio puro — **PRÓXIMA**
- [ ] `Money` (parse estrito, aritmética com overflow, moeda, JSON) + fuzz
- [ ] `Wallet` (criação/reidratação, débito/crédito, versão)
- [ ] `WagerTransaction` (tipos, máquina de estados, origem interna/externa, OPENING)
- [ ] `LedgerEntry` (validação balanceAfter)
- [ ] Eventos de domínio tipados + envelope
- [ ] Hash de idempotência (JCS) com vetores golden
- [ ] Erros classificáveis + códigos de falha estáveis
- [ ] Teste AST "sem float" + depguard (domínio não importa fx/http/aws/pgx)

### F2 — Persistência — pendente
- [ ] Migrations goose (up/down) + `jungle migrate up|down|status`
- [ ] Constraints, índices parciais, FK composta ledger→transação
- [ ] Triggers: imutabilidade do ledger, terminal imutável, cadeia do ledger, saldo=último lançamento (DEFERRED)
- [ ] REVOKE para `jungle_app`; `FOR NO KEY UPDATE`
- [ ] Repositórios pgx + TxManager + classificação de erros PG (transitório × permanente)
- [ ] Testes de integração das constraints (DB real)

### F3 — Casos de uso núcleo — pendente
- [ ] OpenWallet (OPENING + ledger + outbox no mesmo commit; saldo 0 sem OPENING)
- [ ] Submit BET/WIN/LOSS síncrono com idempotência (`ON CONFLICT DO NOTHING`)
- [ ] Concorrência: 50× mesma aposta; 80+80 sobre 100; carteiras paralelas

### F4 — Reversões e referências pendentes — pendente
- [ ] REFUND/ROLLBACK com validação de referência e reversão única
- [ ] PENDING_REFERENCE + worker (SKIP LOCKED, backoff, TTL, wake-up de dependentes)

### F5 — HTTP + AuthN/Z — pendente
- [ ] Rotas do contrato, problem+json, códigos documentados
- [ ] OIDC (issuer público × JWKS interno, aud, scopes, provider_id) + isolamento (404)
- [ ] OpenAPI + docs em `/docs`

### F6 — Consumer SQS + inbox — pendente
- [ ] Inbox na mesma transação; delete pós-commit; DLQ explícita p/ veneno; backoff por visibility
- [ ] Bloqueio de grupo FIFO no batch; shutdown seguro
- [ ] Cenários cruzados HTTP×SQS

### F7 — Outbox relay — pendente
- [ ] Claim por cabeça de partição + lease + fencing token; publish SNS FIFO fora da transação; dead_at
- [ ] Recuperação entre commit/publish e publish/mark

### F8 — Observabilidade e reconciliação — pendente
- [ ] Logs JSON com allowlist; métricas Prometheus; health
- [ ] Reconciliação (REPEATABLE READ, `lag()`), sweeper periódico
- [ ] Grafana/Prometheus (profile `obs`)

### F9 — E2E multi-instância e falhas — pendente
- [ ] 3 processos + barreira `pg_stat_activity`
- [ ] Proxies de falha (SQS sem delete, PG fora) + `faultinject`
- [ ] Os 8 cenários obrigatórios + `-race`

### F10 — Lab público — pendente
- [ ] `compose.lab.yaml` (hostname público, limites, restart)
- [ ] Terraform `edge-lab` (rota Caddy formato `preview`, reload via admin API)
- [ ] Smoke test contra `https://jungle.lab.fredzol.io`

### F11 — Documentação e entrega — pendente
- [ ] README (pré-requisitos, env, filas, migrations, exemplos, testes)
- [ ] ARCHITECTURE.md + ADRs + `docs/TESTING.md` (matriz cenário → teste → comando)
- [ ] Teste de carga (k6) + relatório

## 5. Lições dos forks (checklist de regressão)

- [ ] Índice de reversão **sem** `kind` (REFUND+ROLLBACK na mesma BET = rejeitado)
- [ ] Todo `SKIP LOCKED` dentro de transação com UPDATE de claim
- [ ] Nunca I/O de rede com transação SQL aberta
- [ ] `MessageGroupId = walletId`; claim da outbox por cabeça de partição; batch FIFO bloqueia grupo após falha
- [ ] Replay devolve saldo observado; mesmo externalId com chave nova → 409
- [ ] Validar assinatura, `iss`, `aud`; 404 para transação de outro provider
- [ ] Testes sem mocks de PG/SQS/IdP; processos reais

## 6. Spikes — resultados

| Spike | Resultado | Consequência |
|---|---|---|
| IAM com `AUTH=true` (MiniStack 1.5.19) | Allow/deny respeitados em SQS e SNS; chave desconhecida → `UnrecognizedClient` (403) | Least privilege por componente é real no ambiente local |
| `SenderId` | Vem `000000000000` (account), não o `AIDA…` do usuário | D10: mapa de remetentes com curinga do account no emulador |
| Redrive FIFO | 5 recebimentos sem delete → 6º vazio, DLQ com 1 mensagem | `maxReceiveCount=5` confiável para testes de DLQ |
| SNS FIFO → SQS (raw) | Entrega raw ok; republicação com mesmo `MessageDeduplicationId` deduplicada | Outbox pode republicar com o mesmo `eventId` |
| Issuer Keycloak | `iss=http://localhost:18080/auth/realms/jungle` tanto via edge quanto via `keycloak:8080`; `aud=jungle-api`; `provider_id` e scopes corretos; client shortlived com TTL 5 s | API valida `iss` público + JWKS interno |
| Persistência | `make down` + `make up`: MiniStack restaura estado; Terraform "No changes" | Liga/desliga sem perder filas/IAM |
| Memória | Stack completo ~640 MB (Keycloak ~460 MB) | Cabe com folga na VM |

Observação: o Keycloak responde 503 (bootstrap) por alguns segundos depois do health UP → `provision.sh` faz retry do apply.

## 7. Log de progresso

| Data (UTC) | Fase | O que foi feito | Próximo passo |
|---|---|---|---|
| 2026-10-01 | — | Pesquisa, arquitetura e estudo de 12 forks concluídos | Iniciar F0 |
| 2026-10-01 | F0 | Fundação completa: compose isolado, Terraform provisionando Keycloak/MiniStack/Postgres, esqueleto Fx com health, edge Traefik, spikes executados | F1: domínio puro (Money primeiro) |
