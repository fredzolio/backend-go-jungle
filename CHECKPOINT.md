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
   Testes: `make test-race` (unitários), `make test-integration` (Docker; containers efêmeros via testcontainers), `make test-system` (processos independentes + falhas) e `make test-e2e` (stack no ar; tokens reais do Keycloak).
   Verificação estática: `GOTOOLCHAIN=go1.27.1 gopls check $(find cmd internal migrations -name '*.go')`.
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
| Exposição pública | 1 rota no Caddy do host: `jungle.lab.fredzol.io → 127.0.0.1:18080` (`make lab-expose` / `make lab-unexpose`, Terraform `edge-lab`) |
| Config do lab | `.env.lab` (0600, fora do git; `make lab-init`); usado automaticamente pelo Makefile |

| Ação | Comando |
|---|---|
| Ligar | `make up` |
| Desligar (mantém dados) | `make down` |
| Desligar e apagar **tudo** (rota pública, containers, volumes, imagens locais) | `make destroy` |
| Publicar / despublicar | `make lab-expose` / `make lab-unexpose` (reiniciam o Caddy do host: ~1 s para todos os sites) |
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

### F1 — Domínio puro — **CONCLUÍDA** (2026-10-01)
- [x] `Money` (parse estrito, aritmética com overflow, moeda, JSON só com string) + fuzz (~340k execs)
- [x] `Wallet` (abertura com/sem saldo, reidratação, débito/crédito, versão só muda com saldo)
- [x] `WagerTransaction` (tipos, máquina de estados, origem interna/externa, OPENING, prazo/tentativas de referência)
- [x] `LedgerEntry` (valida `balanceAfter = balanceBefore ± amount`, não negativo)
- [x] Regras de decisão `Evaluate` (soma selada Move/NoMove/Reject/AwaitReference) para os 5 tipos + reversões
- [x] Eventos tipados + envelope (tipo/versão fixados no construtor; RFC 3339 UTC; dinheiro em string)
- [x] Hash de idempotência (JCS) validado contra vetores golden gerados por implementação independente (Python)
- [x] Erros classificáveis (`errors.Is/As`, `ValidationError` por campo) + códigos de falha estáveis
- [x] Guardas: teste AST "sem float" (exceto `platform/metrics/`) + limite de imports do domínio (stdlib + uuid + domain)

Interpretações registradas na F1 (para o ARCHITECTURE.md):
- Identificadores externos restritos a `[A-Za-z0-9._:-]{1,128}` (deixa o JSON canônico sem escapes).
- UUIDs aceitos em qualquer caixa, normalizados para minúsculas antes do hash; valores monetários sem normalização (só a forma canônica `0.00` é aceita).
- `BET`/`LOSS` não aceitam referência; `WIN` aceita referência opcional a uma `BET` processada da mesma rodada (ausente → espera).
- Uma transação é revertida com sucesso **no máximo uma vez**, qualquer que seja o tipo (`ALREADY_REVERSED`); `ROLLBACK` de `ROLLBACK` não é permitido.
- `ROLLBACK` de `BET` credita; de `WIN`/`REFUND` debita (falta de saldo → `REVERSAL_INSUFFICIENT_FUNDS`).
- Moedas suportadas: BRL, USD, EUR (todas com 2 casas).

### F2 — Persistência — **CONCLUÍDA** (2026-10-01)
- [x] Migrations goose embutidas (up/down) + `jungle migrate up|down|reset|status`; serviço `migrate` no compose (role `jungle_owner`) antes das APIs; `make migrate-status|migrate-up|migrate-down`
- [x] Constraints: CHECKs por origem/tipo/estado, índices únicos parciais (idempotência por provider, uma OPENING por carteira, **reversão bem-sucedida única por referência, sem `kind`**), FK composta ledger→transação `(id, wallet, currency, amount)` e ledger→carteira `(id, currency)`
- [x] Triggers: carteira (identidade imutável, versão +1 só com saldo, sem DELETE/TRUNCATE), transação (terminal imutável, identidade imutável, não volta a PENDING), ledger (append-only, cadeia contínua), outbox (conteúdo imutável), e **constraint triggers DEFERRED** saldo/versão da carteira = último lançamento
- [x] Privilégios: `jungle_app` só INSERT/SELECT no ledger, sem DELETE em nada, sem acesso à tabela do goose (conferido no ambiente real)
- [x] Session timeouts por conexão (`lock_timeout` 5s, `statement_timeout` 15s, `idle_in_transaction_session_timeout` 30s); `FOR NO KEY UPDATE` na carteira
- [x] Ports da aplicação (`UnitOfWork`, stores) + adapters pgx (wallets, transactions, ledger, outbox) + classificação de erros (`ErrTransient`/`ErrIntegrity`/`ErrNotFound`/`ErrWalletExists`/`ErrConcurrentUpdate`)
- [x] Harness de integração (`internal/testsupport/pgtest`, build tag `integration`): Postgres real via testcontainers, papéis espelhando o Terraform, template migrado clonado por teste
- [x] 14 testes de integração: migrations reversíveis, invariantes da carteira, ledger append-only (owner→trigger, app→sem privilégio), FK composta, cadeia, divergência carteira×ledger no commit, imutabilidade/OPENING única/reversão única, idempotência por provider, wallet inexistente, versão obsoleta, lock timeout → transitório, paginação do ledger

Notas da F2:
- A FK de `wager_transactions` é só `wallet_id` (não `(wallet_id, currency)`): uma rejeição `CURRENCY_MISMATCH` precisa ser persistida para auditoria; a consistência de moeda das movimentações reais fica nas FKs compostas do ledger.
- Bug real encontrado pelos testes de integração e corrigido: `CASE` com `NEW.wallet_id` no trigger diferido falhava em linhas de `wallets` (PL/pgSQL) → `IF/ELSE`. A migration 00003 foi editada antes da entrega; o banco local foi resetado com `migrate reset` + `up` (demonstra a reversão).
- Mutação de controle: com `kind` no índice de reversão, o teste de reversão única falha (como esperado).

### F3 — Casos de uso núcleo — **CONCLUÍDA** (2026-10-01)
- [x] `Wallets.Open`: carteira + OPENING `PROCESSED` + lançamento + `WagerTransactionProcessed` + `WalletBalanceChanged` no mesmo commit (versão 1); saldo 0 sem OPENING/ledger/eventos; duplicata → `ErrWalletExists`
- [x] `Wagering.Submit` síncrono (um commit, nenhum `PENDING` intermediário): `INSERT … ON CONFLICT DO NOTHING` → replay (mesma chave+hash) / `ErrIdempotencyConflict` (mesma chave, hash diferente) / `ErrDuplicateExternalTransaction` (mesmo externalId com outra chave); lock da carteira; `Evaluate`; ledger + saldo + status + outbox; `WakeDependents`
- [x] Liquidação (`settle`) compartilhada para o worker de referências (F4): Move / NoMove / Reject (persistido com saldo observado) / AwaitReference (`PENDING_REFERENCE` + evento só na primeira vez; backoff exponencial com jitter inteiro)
- [x] Gancho `Within` no Submit para o consumer SQS registrar a inbox no mesmo commit (F6)
- [x] Testes de integração: abertura com/sem saldo e duplicada, BET/WIN/LOSS (LOSS sem ledger e sem mudar versão), replay devolve saldo original, conflitos, rejeições auditáveis (`INSUFFICIENT_FUNDS`, `CURRENCY_MISMATCH`, `WALLET_PLAYER_MISMATCH`), carteira inexistente e provider divergente sem persistir nada
- [x] Concorrência (in-process, DB real): **50× mesma aposta → 1 débito**; **80+80 sobre 100 → 1 processada, 1 `INSUFFICIENT_FUNDS`, saldo 20, 1 débito** (10 rodadas + reenvios); **carteira B processa enquanto A está travada**; 8 carteiras × 10 apostas simultâneas consistentes. Estável com `-count=4`
- [x] Mutação de controle: sem `FOR NO KEY UPDATE`, a guarda de versão bloqueia o lost update e o teste 80+80 falha (defesa em profundidade comprovada)

### F4 — Reversões e referências pendentes — **CONCLUÍDA** (2026-10-01)
- [x] REFUND/ROLLBACK no `Submit`: referência resolvida e travada (`FOR UPDATE`) depois da carteira; `ALREADY_REVERSED` (domínio + índice único); `REVERSAL_INSUFFICIENT_FUNDS`; `REFERENCE_NOT_PROCESSED` imediato para referência rejeitada
- [x] `Wagering.ResolveDue`: candidatas listadas sem lock; cada uma em transação própria com carteira `SKIP LOCKED` → transação `FOR UPDATE` → recheca status/agenda (mesma ordem do Submit: sem deadlock, sem lease para vazar)
- [x] Expiração: referência ausente → `REFERENCE_NOT_FOUND`; presente mas não concluída → `REFERENCE_NOT_PROCESSED`; próxima tentativa nunca passa do prazo; evento `PendingReference` só na primeira vez
- [x] Worker genérico `workers.Poller` no ciclo de vida do Fx (contexto próprio, cancelamento faz rollback, término observável); role `resolver` via `ROLES`; configurável (`REFERENCE_TTL`, backoffs, intervalo, lote)
- [x] Testes de integração com relógio controlável: REFUND antes da BET resolvido por outra instância; expiração com reagendamento (tentativas, sem evento duplicado); regras de reversão (dupla, ROLLBACK de ROLLBACK, parcial, outra rodada, ROLLBACK de REFUND); ROLLBACK de WIN sem saldo; **REFUND × ROLLBACK concorrentes → dinheiro devolvido uma vez**; **3 resolvers concorrentes → cada pendência concluída exatamente uma vez**. Estável com `-count=4`
- [x] Ambiente real: resolver ativo nas 3 instâncias; SIGTERM encerra worker → HTTP → pool

### F5 — HTTP + AuthN/Z — **CONCLUÍDA** (2026-10-01)
- [x] Rotas do contrato (`net/http` ServeMux): `POST /wallets`, `GET /wallets/{id}`, `GET /wallets/{id}/ledger` (cursor opaco ligado à carteira, `limit` 1–200), `POST /wagering/transactions`, `GET /wagering/transactions/{id}`, `GET /providers/{p}/wagering/transactions/{ext}`, health, `/openapi.yaml`, `/docs` (Scalar)
- [x] Contrato de respostas: 200 PROCESSED (inclusive replays) · 202 PENDING_REFERENCE + `Location` · 422 REJECTED + `failureCode` · 400 `INVALID_REQUEST`/`RESERVED_KIND` (lista de campos) · 401 `UNAUTHENTICATED` (+`WWW-Authenticate`) · 403 `INSUFFICIENT_SCOPE`/`PROVIDER_MISMATCH` · 404 `WALLET_NOT_FOUND`/`TRANSACTION_NOT_FOUND` · 409 `IDEMPOTENCY_KEY_REUSED`/`DUPLICATE_EXTERNAL_TRANSACTION`/`WALLET_ALREADY_EXISTS` · 503 `TEMPORARILY_UNAVAILABLE` + `Retry-After` · erros em `application/problem+json`
- [x] OIDC (`go-oidc`): issuer público × JWKS interno, `aud=jungle-api`, RS256, `typ=Bearer`, scopes, claim `provider_id`; operações de carteira só com scopes internos; provider só vê o que é dele (404, sem revelar existência); `providerId` do corpo tem que bater com o token
- [x] Middleware: correlation id (aceita `X-Correlation-Id` válido ou gera UUIDv7, ecoa no header), log JSON por request (sem corpo nem credenciais; com `clientId`/`providerId`), recover de panic; corpo JSON estrito (campos desconhecidos, dados extras, 64 KB, número no lugar de string → 400)
- [x] OpenAPI 3.1 (`api/openapi.yaml`, válido no Redocly) embutido no binário
- [x] Testes de integração do contrato (Postgres real + verificação JWT real com JWKS local): 7 casos de token inválido, autorização interna × provider, abertura/conflito, todos os status do submit, isolamento entre providers em leituras e replays, paginação por cursor
- [x] **E2E com o Keycloak real** pelo edge (`make up && make test-e2e`): token ausente/adulterado/expirado (client de 5 s) → 401; fluxo completo com isolamento, replay e 80+80

### F6 — Consumer SQS + inbox — **CONCLUÍDA** (2026-10-01)
- [x] `Wagering.Ingest`: mesmo caso de uso do HTTP; inbox `(consumer, messageId)` gravada **na mesma transação** (gancho `Within`); redelivery = replay + inbox duplicada; mesmo `messageId` com conteúdo diferente → `ErrMessageConflict` (rollback, DLQ)
- [x] Envelope estrito (`WagerTransactionRequested`, `messageId`, RFC 3339, `data`), hash canônico do envelope (identidade + payload hash + chave)
- [x] Autorização do remetente: `SenderId` × `senders.json` (Terraform) + contas confiáveis (MiniStack: account como SenderId, D10); producer não autorizado → DLQ
- [x] Classificação: commit → delete; permanente (`MALFORMED`, `UNAUTHORIZED_SENDER`, `IDEMPOTENCY_KEY_REUSED`, `DUPLICATE_EXTERNAL_TRANSACTION`, `PROVIDER_MISMATCH`, `WALLET_NOT_FOUND`, `MESSAGE_ID_CONFLICT`, `INTEGRITY_VIOLATION`, `RESERVED_KIND`) → envio explícito à DLQ com `errorCode` + delete; demais → mantém com visibility backoff exponencial (1s…60s) e redrive após `maxReceiveCount=5`
- [x] FIFO: `MessageGroupId=walletId`; falha numa mensagem bloqueia o grupo no batch e libera as seguintes (visibility 0); rejeição de negócio é terminal (delete)
- [x] Shutdown: cancelamento para o long-poll na hora; mensagem em andamento termina (`PROCESS_TIMEOUT` 20s < visibility 60s) com contexto próprio; restante do batch liberado; delete/DLQ com contexto próprio
- [x] `workers.Loop` (N pollers por processo, `CONSUMER_POLLERS=2`), role `consumer`, `awsx.QueueURL` com retry (SQS indisponível no start não derruba o processo)
- [x] Integração (MiniStack + Postgres reais, testcontainers): aplicação + inbox + delete; redelivery move uma vez; **HTTP × SQS = uma movimentação**; veneno → DLQ (`MALFORMED`×2, `MESSAGE_ID_CONFLICT`); remetente não autorizado; **cenário 5: commit e morte antes do delete → redelivery segura em outro consumer**; ordem FIFO preservada após falha transitória. Estável com `-count=3`
- [x] E2E no stack real: produtor IAM do `provider-a` (AUTH=true) → SQS → carteira; cópia HTTP é replay; producer não consegue consumir a fila

### F7 — Outbox relay — **CONCLUÍDA** (2026-10-01)
- [x] `OutboxRelay.Claim` em um único `UPDATE` autocommit: cabeça de cada partição (carteira) via `DISTINCT ON`, `FOR UPDATE SKIP LOCKED`, lease (`locked_until`) e **fencing token** (`claim_id`); cabeça arrendada, em backoff ou **estacionada (dead)** bloqueia a partição (ordem acima de disponibilidade)
- [x] `app.Relay`: claim → publish **fora de transação SQL** → `MarkPublished` cercado por `claim_id`; falha → backoff exponencial (1s…5min) e `dead_at` após `OUTBOX_MAX_ATTEMPTS` (20); lease perdido só gera republicação (mesmo `eventId`)
- [x] `snspublisher`: `PublishBatch` (lotes de 10) em `wager-events.fifo`; `MessageGroupId=walletId`, `MessageDeduplicationId=eventId`, atributos `eventType`/`eventVersion`; resultado por evento
- [x] Terraform grava `aws/events_topic_arn`; role `outbox` (worker `outbox-relay` via `workers.Poller`); credencial IAM própria do publisher
- [x] Integração (Postgres + MiniStack SNS→SQS reais, relógio controlável): eventos commitados sem relay (**crash entre commit e publicação**) publicados uma vez em ordem por carteira; **2 relays disputando** → tudo publicado, sem duplicata, ordem preservada; **crash entre publicação e confirmação** → outro relay assume após o lease e republica com o mesmo `eventId` (assinante recebe uma vez, `attempts=2`); falha persistente → backoff → dead, bloqueando a partição. Estável com `-count=3`
- [x] E2E: evento `WalletBalanceChanged` chega à fila `wager-events-audit.fifo` com o contrato do envelope
- [x] Achado do e2e com IAM aplicado: MiniStack exige `sns:PublishBatch` explicitamente (AWS usa `sns:Publish`) → policy lista as duas; o relay ficou em backoff sem perder nada e drenou sozinho após o reprovisionamento

### F8 — Observabilidade e reconciliação — **CONCLUÍDA** (2026-10-01)
- [x] Logs JSON com **allowlist de atributos** (valores, saldos, corpos e credenciais nunca saem, mesmo por engano); identificadores `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`
- [x] Port `app.Metrics` (Nop nos testes) + Prometheus em porta interna `:9090` (não roteada pelo edge — `/metrics` público = 404): transações por canal/tipo/status/replay, latência (histograma), **conflitos de concorrência** (`ErrContention`: lock timeout/deadlock/serialização/corrida otimista), mensagens SQS por resultado e duplicata, tentativas de referência, outbox publicados/falhas/dead, **atraso da outbox** (idade do mais antigo pendente), profundidade da DLQ, pendências de referência, reconciliações por resultado, requests HTTP por rota/status
- [x] Reconciliação `POST /wallets/{id}/reconciliation` (scope `wallets.reconcile`): snapshot `REPEATABLE READ READ ONLY`, saldo reconstruído do ledger + **verificação de cadeia com `lag()`** (`chainIntact` extra), `difference = stored − calculated`; divergência → resposta, log `ERROR` e métrica; nada é alterado
- [x] Sweep periódico (role `reconciler`, só no `api-1`) sobre todas as carteiras
- [x] Profile `obs` (`make obs-up`): Prometheus `v3.15.0` (127.0.0.1:19090, 5 regras de alerta validadas com promtool: divergência, DLQ, atraso da outbox, evento estacionado, pendências) + Grafana `13.2.3` (127.0.0.1:13000, datasource e dashboard provisionados)
- [x] **Teste da composição Fx**: `fx.ValidateApp` com todas as roles (mutação comprovou que pega provider faltando) + **teste de integração do ciclo de vida** (Postgres + MiniStack reais, todas as roles, start → serve → stop, `goleak` sem vazamento)
- [x] Testes: allowlist do logger; reconciliação consistente após operações reais; **divergência de saldo e quebra de cadeia detectadas** (forçadas por superusuário com `session_replication_role=replica`, a única forma de burlar os triggers — limitação documentada); sweep conta divergências; endpoint HTTP interno-only

Achado da F8: duas substituições de texto no bootstrap falharam em silêncio (Reconciler e métricas fora do grafo) e só apareceram no boot → origem do teste `fx.ValidateApp`.

### F9 — E2E multi-instância e falhas — **CONCLUÍDA** (2026-10-01)
- [x] Pontos de falha por build tag (`internal/platform/faults`): com `-tags faultinject`, `JUNGLE_FAULTS=<ponto>` mata o processo (exit 137, sem cleanup) em `consumer.after_commit`, `relay.after_publish`, `resolver.before_commit`; no binário de produção é no-op
- [x] Harness de sistema (`test/system`, tag `system`, `make test-system`): compila o binário real com **`-race` + faultinject**, sobe **processos de SO independentes** (memória e pool próprios) contra Postgres e MiniStack reais e IdP local com JWKS; liga/mata (SIGKILL)/reinicia processos; falha se qualquer processo imprimir `DATA RACE` ou não encerrar limpo no SIGTERM; checagem final saldo = ledger (e versão) para todas as carteiras
- [x] Cenário 1+4: mesma aposta 50× em paralelo distribuída entre 3 processos → 1 débito
- [x] Cenário 2+4: 80+80 sobre 100 em processos diferentes, com **barreira em `pg_stat_activity` provando que os 2 processos esperaram o mesmo lock** → 1 processada, 1 rejeitada, saldo 20, 1 débito (3 rodadas)
- [x] Cenário 3+4: 6 carteiras × 60 apostas simultâneas em 3 processos
- [x] HTTP (processo A) × SQS (consumers B/C) da mesma operação → uma movimentação
- [x] Cenário 5: processo consumer morto **após o commit e antes do delete** → redelivery tratada por outro processo sem nova movimentação
- [x] Cenário 6: processo relay morto **entre publicação e confirmação** → outro processo republica após o lease (mesmo `eventId`)
- [x] Cenários 7+8: REFUND `PENDING_REFERENCE` aceito por um processo que é morto → retomado por outro quando a BET chega
- [x] Cenário 8: **todos os processos mortos** → novos processos devolvem o resultado original no replay, retomam pendência e a reconciliação fica consistente
- [x] Estável com `-count=2`; e2e verde com a imagem de produção

Achado da F9: `pg_stat_activity` esconde `query`/`wait_event` de outros papéis → a barreira consulta como superusuário.

### F10 — Lab público — **CONCLUÍDA** (2026-10-01)
- [x] `make lab-init` gera `.env.lab` (0600, fora do git) com `PUBLIC_BASE_URL=https://jungle.lab.fredzol.io` e segredos de bootstrap aleatórios; o Makefile usa `.env.lab` automaticamente quando existe (no lugar de um `compose.lab.yaml`: só a URL e os segredos mudam)
- [x] Ambiente recriado do zero com os segredos fortes (`make destroy && make up`)
- [x] Terraform `stacks/edge-lab` (state próprio `edge-lab.tfstate`, serviço `edge-lab`, profile `edge`): grava `/etc/caddy/conf.d/jungle.caddy` **no formato do `preview`** (aparece no `preview ls`) e valida o Caddyfile com o Caddy CLI 2.6.2; `make lab-expose` / `make lab-unexpose` aplicam e então reiniciam o Caddy do host; `make destroy` remove a rota primeiro
- [x] Superfície pública mínima (Traefik): API, `/docs`, `/openapi.yaml` e, do Keycloak, só `token`, `certs` e `.well-known` do realm `jungle`; console admin, realm master, account e `/metrics` → 404; negócio sem token → 401. Admin do Keycloak: túnel `ssh -L 18090:127.0.0.1:18090 lab` → `http://localhost:18090/auth/admin`
- [x] Certificado Let's Encrypt emitido; **suíte e2e completa verde pelo HTTPS público** (tokens do Keycloak com `iss=https://jungle.lab.fredzol.io/auth/realms/jungle`)

**Incidente (2026-10-01 19:27 UTC, ~1 min):** o primeiro desenho recarregava o Caddy do host pela admin API (`caddy reload`). O Caddy **2.6.2 do Ubuntu entra em panic em reloads** (`context: internal error: missing cancel error`) e a unit não tem `Restart=`, então `lab.fredzol.io`/`oc.fredzol.io` caíram até eu executar `sudo systemctl start caddy`. O `systemctl reload caddy` usado pelo `preview add` passa pelo mesmo caminho e também derrubaria o Caddy. Correção aplicada aqui: o stack só valida; o Makefile faz `systemctl restart`. **Recomendações para o repo zoliolab (fora deste escopo, não alteradas):** atualizar o Caddy (pacote oficial `caddy` ≥ 2.8) e adicionar `Restart=on-failure` à unit; trocar o `reload` do `preview` por `restart` enquanto isso.

### F11 — Documentação e entrega — **PRÓXIMA**
- [ ] README (pré-requisitos, env, filas, migrations, exemplos, testes)
- [ ] ARCHITECTURE.md + ADRs + `docs/TESTING.md` (matriz cenário → teste → comando)
- [ ] Teste de carga (k6) + relatório

## 5. Lições dos forks (checklist de regressão)

- [x] Índice de reversão **sem** `kind` (domínio na F1, índice no banco na F2, com teste e mutação de controle)
- [x] Todo `SKIP LOCKED` dentro de transação (resolver: carteira `SKIP LOCKED` + transação travada na mesma transação SQL)
- [x] Nunca I/O de rede com transação SQL aberta (relay: claim commitado antes do publish; consumer: delete/DLQ após o commit)
- [x] `MessageGroupId = walletId`; batch FIFO bloqueia grupo após falha (F6); claim da outbox por cabeça de partição (F7)
- [x] Replay devolve saldo observado; mesmo externalId com chave nova → `ErrDuplicateExternalTransaction` (409 na F5)
- [x] Validar assinatura, `iss`, `aud`; 404 para transação de outro provider
- [x] Testes sem mocks de PG/SQS/IdP; processos reais (integração, e2e com Keycloak real, sistema com processos independentes)

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
| 2026-10-01 | F1 | Domínio puro completo (money, wallet, wagering, events) com testes unitários, fuzz, vetores golden e guardas arquiteturais; `go test -race` verde | F2: migrations goose + constraints/triggers + repositórios pgx |
| 2026-10-01 | F10 | https://jungle.lab.fredzol.io no ar (Terraform edge-lab, superfície mínima, e2e pelo HTTPS); incidente do reload do Caddy 2.6.2 corrigido e documentado | F11: README, ARCHITECTURE, ADRs, TESTING, carga |
| 2026-10-01 | F9 | Os 8 cenários obrigatórios com processos independentes (-race, faultinject), contenção comprovada por pg_stat_activity | F10: exposição em jungle.lab.fredzol.io |
| 2026-10-01 | F8 | Logs com allowlist, métricas Prometheus, reconciliação com verificação de cadeia, sweep, Prometheus/Grafana, validação e ciclo de vida do Fx | F9: 3 processos + falhas |
| 2026-10-01 | F7 | Outbox relay multi-instância (SNS FIFO, lease + fencing, ordem por carteira, recuperação de crash); cenário 6 verde | F8: métricas, logs, reconciliação |
| 2026-10-01 | F6 | Consumer SQS com inbox transacional, DLQ, ordem FIFO, shutdown seguro; cenário 5 e cruzamento HTTP×SQS verdes | F7: outbox relay (SNS FIFO) |
| 2026-10-01 | F5 | API HTTP + OIDC + OpenAPI; testes de contrato e e2e com Keycloak real verdes | F6: consumer SQS + inbox + DLQ |
| 2026-10-01 | F4 | Reversões e worker de PENDING_REFERENCE (multi-instância, TTL, backoff, wake-up); cenário obrigatório 7 verde | F5: HTTP + OIDC + OpenAPI |
| 2026-10-01 | F3 | Casos de uso OpenWallet/Submit com idempotência persistente e liquidação compartilhada; cenários de concorrência obrigatórios (1, 2, 3) verdes contra Postgres real | F4: reversões + worker de PENDING_REFERENCE |
| 2026-10-01 | F2 | Persistência completa: 5 migrations reversíveis, invariantes no banco, privilégios mínimos, stores pgx, harness testcontainers + 14 testes de integração; gopls v0.23.0 instalado (`~/go/bin`, symlink em `~/.cargo/bin`) | F3: OpenWallet + Submit (BET/WIN/LOSS) + concorrência |
