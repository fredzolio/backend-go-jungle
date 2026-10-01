# Arquitetura

Serviço único em Go 1.27.1, com Uber Fx, que move carteiras a partir de operações de provedores
recebidas por HTTP e por SQS FIFO. As garantias financeiras vivem no PostgreSQL, os eventos saem por
uma outbox transacional para SNS FIFO e a autenticação é OAuth 2.0 (client credentials) no Keycloak.

```
Provedor ──HTTPS──► Traefik (edge) ──► api-1..3 ──┐            ┌──► SNS wager-events.fifo ──► SQS assinantes
Provedor ──SQS FIFO wager-transactions.fifo ──────┤ PostgreSQL ├─ outbox relay (todas as instâncias)
                                                  │  (ledger,  │
Keycloak (realm jungle) ◄── JWKS ─────────────────┘  inbox,    │
                                                     outbox)   │
```

## Organização do código

| Pacote | Responsabilidade |
|---|---|
| `internal/domain/{money,wallet,wagering,events}` | Domínio puro: só stdlib + `uuid` (garantido por `archtest`) |
| `internal/app` | Casos de uso e ports: `Wallets`, `Wagering` (Submit, Ingest, ResolveDue), `Relay`, `Reconciler`, `Queries` |
| `internal/adapters/postgres` | pgx + SQL explícito, unit of work, classificação de erros, migrations |
| `internal/adapters/httpapi`, `oidc`, `sqsconsumer`, `snspublisher`, `awsx` | Transporte e integrações |
| `internal/workers` | `Poller` e `Loop` ligados ao ciclo de vida do Fx |
| `internal/bootstrap` | Único lugar que conhece todos os módulos Fx |
| `migrations/` | SQL versionado (goose), embutido no binário |
| `infra/terraform` | Plano de controle: realm/clients, filas/tópico/IAM, papéis do Postgres, rota pública |

## Dinheiro

- `Money` é imutável: `int64` em unidades mínimas mais a moeda (BRL, USD, EUR — moedas ISO 4217 com
  2 casas). Faixa: ±92.233.720.368.547.758,07.
- **Contrato externo** `{"amount":"25.00","currency":"BRL"}`. O parser aceita só
  `^(0|[1-9]\d{0,16})\.\d{2}$`, o que rejeita vazio, `NaN`, `Infinity`, notação científica, escala
  diferente de 2, sinal, zeros à esquerda e espaços. O JSON exige `amount` como string, então um
  número JSON é rejeitado e nenhum valor passa por float. **Nada é normalizado**: a única forma aceita
  já é canônica para o hash.
- Soma, subtração, negação e comparação exigem a mesma moeda e checam overflow. Valores negativos
  existem em diferenças (reconciliação), nunca no saldo.
- **Persistência:** `amount_minor BIGINT` + `currency CHAR(3)`.
- Um teste de AST proíbe `float32`/`float64`/`ParseFloat` em todo o `internal/`. A única exceção é
  `platform/metrics`, onde há segundos para o Prometheus e nenhum dinheiro.

## Modelo e invariantes no banco

As invariantes valem **independentemente do código**:

| Invariante | Mecanismo |
|---|---|
| Saldo ≥ 0, versão ≥ 1 | `CHECK` em `wallets` |
| Versão +1 exatamente quando o saldo muda; identidade imutável; carteira nunca apagada | trigger `wallets_guard` |
| Uma carteira por (jogador, moeda) | `UNIQUE (player_id, currency)` |
| Idempotência por provider | `UNIQUE (provider_id, external_transaction_id)` e `UNIQUE (provider_id, idempotency_key)` (parciais, origem EXTERNAL) |
| No máximo uma OPENING por carteira | índice único parcial |
| **Uma transação é revertida com sucesso no máximo uma vez** (REFUND *ou* ROLLBACK) | `UNIQUE (reference_transaction_id) WHERE status='PROCESSED' AND kind IN ('REFUND','ROLLBACK')` |
| Forma por origem (INTERNAL não tem campos de provider), valor por tipo (LOSS = 0, demais > 0), referência obrigatória em REFUND/ROLLBACK, `failure_code` ⇔ REJECTED/FAILED, PROCESSED tem resultado | `CHECK`s |
| Transação terminal imutável; identidade imutável; não volta a PENDING; nunca apagada | trigger `wager_transactions_guard` |
| Lançamento: `after = before ± amount`, `amount > 0`, nada negativo | `CHECK` |
| Um lançamento por (carteira, transação) e por (carteira, versão) | `UNIQUE` |
| Lançamento casa com carteira, moeda e valor da transação | FK composta `(transaction_id, wallet_id, currency, amount_minor)` |
| Cadeia contínua: `before` = `after` anterior, versão = anterior + 1 | trigger `BEFORE INSERT` |
| Ledger append-only | triggers `UPDATE/DELETE/TRUNCATE` + `REVOKE` para `jungle_app` |
| No commit, saldo e versão da carteira = último lançamento | **constraint triggers `DEFERRABLE INITIALLY DEFERRED`** |
| Conteúdo da outbox imutável; pendente não pode ser apagado | trigger `outbox_events_guard` |

**Papéis do banco (Terraform):**
- `jungle_owner`: dono do schema, roda as migrations;
- `jungle_app`: o runtime, sem `UPDATE`/`DELETE` no ledger e sem `DELETE` em nada;
- `jungle_readonly`: leitura.

Limitação documentada: um superusuário consegue desligar triggers com `session_replication_role`. É
exatamente assim que os testes forçam divergências para provar que a reconciliação as detecta.

**Migrations:** goose, aplicadas pelo serviço `migrate` (como `jungle_owner`) antes das APIs.
- `make migrate-status` mostra o estado;
- `make migrate-up` aplica;
- `make migrate-down` reverte a última;
- `jungle migrate reset` reverte todas.

A reversibilidade é testada (`up → reset → up`).

## Transações SQL e concorrência

- **Biblioteca:** `pgx/v5` com SQL explícito. A unit of work (`app.UnitOfWork`) delimita a
  transação: os stores de `app.Tx` compartilham a mesma `pgx.Tx`, e o caso de uso decide o escopo.
  Nível READ COMMITTED; leituras de reconciliação em REPEATABLE READ READ ONLY.
- **Coordenação por carteira (pessimista):** `SELECT … FOR NO KEY UPDATE` na carteira serializa
  escritores *daquela* carteira. Carteiras diferentes nunca esperam umas pelas outras e não existe
  lock global (teste: B processa enquanto A está travada). `NO KEY` não bloqueia as checagens de FK
  dos lançamentos.
- **Defesa em profundidade:** o `UPDATE … WHERE version = $esperada` impede lost update mesmo que o
  lock fosse removido (mutação comprovada), e o `CHECK (balance >= 0)` impede saldo negativo.
- **Ordem de locks** sempre carteira → transação referenciada (Submit e resolver), então não há ciclo
  de deadlock.
- **Timeouts de sessão:** `lock_timeout` 5 s, `statement_timeout` 15 s, `idle_in_transaction` 30 s.
  Um escritor bloqueado falha rápido como `ErrTransient` (com `ErrContention` para as métricas).

## Idempotência

**Fluxo do `Submit`, em uma única transação SQL:**
1. `INSERT … ON CONFLICT DO NOTHING` da linha PENDING, apoiado nos índices únicos, sem
   check-then-insert. Requisições idênticas concorrentes esperam a linha em voo e, após o commit dela,
   leem a vencedora.
2. Em caso de conflito:
   - mesma chave e mesmo hash → **replay**: devolve o resultado persistido, com o saldo observado
     no processamento original;
   - mesma chave e hash diferente → `409 IDEMPOTENCY_KEY_REUSED`;
   - mesmo `(provider, externalTransactionId)` com outra chave → `409 DUPLICATE_EXTERNAL_TRANSACTION`
     (nunca reaplica).
3. Sem conflito: trava a carteira, avalia as regras e aplica.

O replay de um PENDING_REFERENCE devolve 202 com o estado atual.

**Hash (v1):** SHA-256 (hex) do JSON canônico RFC 8785 (JCS) com chaves ordenadas e sem espaços, sobre
`externalTransactionId, gameId, kind, money{amount,currency}, playerId, providerId,
[referenceExternalTransactionId], roundId, walletId`. A referência é omitida quando ausente.

- **Excluídos:** a chave de idempotência e metadados de transporte (headers, envelope SQS).
- **Normalização:** UUIDs em minúsculas; valores não precisam (só a forma canônica é aceita).
- **Identificadores:** restritos a `[A-Za-z0-9._:-]{1,128}`, então o JSON não precisa de escapes e é
  JCS exato.

Os vetores golden em `testdata/` foram gerados por uma implementação independente (Python). O hash é
idêntico para HTTP e SQS.

A chave tem escopo por provider: a mesma string usada por outro provider é outra operação, e nenhum
dado de outro provider vaza num replay.

## Máquina de estados

```
PENDING ──► PROCESSED | REJECTED | FAILED          (terminais, imutáveis)
   │
   └──► PENDING_REFERENCE ──► PROCESSED | REJECTED | FAILED
              └── (nova tentativa agendada: attempts++)
```

- O processamento é **síncrono e sem commit intermediário**: um PENDING nunca é commitado sozinho.
  Morrer antes do commit não deixa nada; o cliente repete com a mesma chave.
- O único estado intermediário durável é **PENDING_REFERENCE**, retomado por qualquer instância.
- A criação valida tudo; a reidratação (`Rehydrate`) só checa consistência, sem reaplicar transições,
  movimentações ou eventos.
- **Transitório × permanente:**
  - transitório: conexão, timeout, `lock_timeout`, deadlock, serialização, `57P0x`, `08xxx`,
    `53xxx`. Nada foi commitado → HTTP 503 + `Retry-After` / SQS com backoff.
  - permanente: violação de integridade (`23xxx`), que indica bug ou adulteração → HTTP 500 / DLQ.
  - FAILED registra falha permanente de infraestrutura para auditoria (`INFRASTRUCTURE_FAILURE`).

## Regras, referências e reversões

| Tipo | Movimento | Regra |
|---|---|---|
| BET | débito | valor > 0; sem saldo → `INSUFFICIENT_FUNDS` |
| WIN | crédito | valor > 0; referência opcional, que se informada precisa ser uma BET processada da mesma rodada (ausente → espera) |
| LOSS | nenhum | valor = 0.00; não cria ledger nem altera versão; emite só `WagerTransactionProcessed` |
| REFUND | crédito | referência a uma BET processada, valor igual |
| ROLLBACK | oposto ao original | BET → crédito; WIN/REFUND → débito. Débito sem saldo → `REVERSAL_INSUFFICIENT_FUNDS`. ROLLBACK de ROLLBACK não é permitido |

- **Referência:** resolvida por `(providerId, referenceExternalTransactionId)` e travada. Provider,
  jogador, carteira, moeda e rodada precisam coincidir (`REFERENCE_MISMATCH`); valor diferente →
  `REVERSAL_AMOUNT_MISMATCH`; tipo não reversível → `REFERENCE_KIND_NOT_REVERSIBLE`.
- **REFUND × ROLLBACK:** uma transação é revertida com sucesso **no máximo uma vez, qualquer que seja o
  tipo** (`ALREADY_REVERSED`, garantido no domínio e no índice). Uma BET reembolsada não aceita
  ROLLBACK e vice-versa, então o débito nunca é devolvido duas vezes. O ROLLBACK de um REFUND restaura
  o débito, mas a BET não volta a ser reembolsável.
- **Referência ainda indisponível:** `PENDING_REFERENCE` + evento `WagerTransactionPendingReference`.
  - O worker `ResolveDue` (todas as instâncias) lista candidatas sem lock; cada uma roda na própria
    transação: carteira `SKIP LOCKED` → transação `FOR UPDATE` → rechecagem. Não há lease para
    vazar: um crash só faz rollback.
  - Backoff exponencial com jitter (1 s … 1 min), nunca passando do prazo; TTL de 10 min.
  - Quando a referência chega, `WakeDependents` antecipa a tentativa para "agora".
  - No vencimento: referência ausente → `REFERENCE_NOT_FOUND`; presente mas não concluída →
    `REFERENCE_NOT_PROCESSED`. Ambas emitem `WagerTransactionRejected`.
  - Referência que terminou REJECTED/FAILED → `REFERENCE_NOT_PROCESSED` imediato; referência
    pendente → continua esperando.

**`failureCode` estáveis** (resultados definitivos e persistidos; HTTP 422):

| Código | Situação |
|---|---|
| `INSUFFICIENT_FUNDS` | BET sem saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | ROLLBACK que precisaria debitar mais que o saldo |
| `CURRENCY_MISMATCH` | moeda da operação diferente da carteira |
| `WALLET_PLAYER_MISMATCH` | carteira não pertence ao jogador |
| `REFERENCE_NOT_FOUND` | referência não chegou dentro do prazo |
| `REFERENCE_NOT_PROCESSED` | referência rejeitada, falhada ou não concluída no prazo |
| `REFERENCE_MISMATCH` | referência com provider/jogador/carteira/moeda/rodada diferentes |
| `REFERENCE_KIND_NOT_REVERSIBLE` | tipo de referência não permitido |
| `REVERSAL_AMOUNT_MISMATCH` | reversão parcial ou com valor diferente |
| `ALREADY_REVERSED` | a referência já tem uma reversão processada |
| `INFRASTRUCTURE_FAILURE` | FAILED |

**Erros corrigíveis não são persistidos** e respondem com `problem+json`:
- `400 INVALID_REQUEST` (com lista de campos), `400 RESERVED_KIND` (OPENING);
- `401 UNAUTHENTICATED`, `403 INSUFFICIENT_SCOPE` / `PROVIDER_MISMATCH`;
- `404 WALLET_NOT_FOUND` / `TRANSACTION_NOT_FOUND`;
- `409` (conflitos de idempotência, carteira duplicada);
- `503 TEMPORARILY_UNAVAILABLE` (+ `Retry-After`).

Contrato completo em [api/openapi.yaml](api/openapi.yaml), também servido em `/docs`.

## Abertura de carteira

Com saldo positivo: carteira (versão 1), OPENING `PROCESSED` (origem INTERNAL, sem metadados de
provider), lançamento de crédito, `WagerTransactionProcessed` e `WalletBalanceChanged`, tudo no mesmo
commit. Com saldo zero: só a carteira. Carteira duplicada → `409 WALLET_ALREADY_EXISTS`.

## Inbox e consumer SQS

**Fila e mensagens:**
- **Fila:** `wager-transactions.fifo`, com visibility 60 s, long poll 20 s e redrive para
  `wager-transactions-dlq.fifo` após `maxReceiveCount=5`.
- **`MessageGroupId = walletId`:** ordem por carteira e paralelismo entre carteiras.
- **`MessageDeduplicationId = messageId`** do envelope. A deduplicação real é feita pela inbox e
  pela idempotência; a do SQS FIFO (5 min) é só um bônus.

**Processamento:**
1. Envelope estrito.
2. Autorização do remetente: `SenderId` × mapa gerado pelo Terraform. No MiniStack o `SenderId` é a
   conta, aceita como curinga — **limitação**: lá o isolamento entre providers no broker vem da
   policy IAM; na AWS usa-se o ID do usuário IAM.
3. Mesmo caso de uso do HTTP, com a **inbox `(consumer, messageId)` gravada na mesma transação** do
   domínio, do ledger e da outbox.
4. Só então `DeleteMessage`.

**Resultados:**
- **Redelivery:** replay mais inbox existente → delete, nada se move.
- **Mesmo `messageId` com conteúdo diferente:** rollback e DLQ (`MESSAGE_ID_CONFLICT`).
- **Rejeição de negócio:** terminal, delete.
- **Permanente** (`MALFORMED`, `UNAUTHORIZED_SENDER`, conflitos, `WALLET_NOT_FOUND`, `RESERVED_KIND`,
  `INTEGRITY_VIOLATION`): envio explícito à DLQ com o atributo `errorCode`, depois delete.
- **Transitório:** a mensagem fica, com visibility em backoff (1 s … 60 s) e redrive automático.
- **Ordem no batch:** uma falha bloqueia o grupo no batch e as seguintes do mesmo grupo são liberadas
  (visibility 0).

**SIGTERM:** o long poll é cancelado na hora; a mensagem em andamento termina com contexto próprio
(`PROCESS_TIMEOUT` 20 s < visibility); o restante do batch é liberado; delete e DLQ usam contexto
próprio. Uma morte entre o commit e o delete gera redelivery segura (testado com processo morto
naquele ponto).

## Outbox transacional

- Estado, saldo, ledger, inbox e eventos são commitados juntos. O payload é o envelope completo,
  snapshot imutável.
- **Relay** (todas as instâncias, role `outbox`):
  1. *claim* num único `UPDATE` autocommit: eventos sem evento anterior pendente na mesma carteira
     (cabeça da partição, via `NOT EXISTS` e índices parciais), `FOR UPDATE SKIP LOCKED`, lease
     (`locked_until`) e **fencing token** (`claim_id`);
  2. publish **fora de qualquer transação SQL**;
  3. confirmação em lote, cercada pelo `claim_id`.
- **Falhas:** backoff exponencial (1 s … 5 min) e `dead_at` após 20 tentativas (com alerta). Uma
  cabeça estacionada bloqueia a carteira: ordem acima de disponibilidade.
- **Crash entre commit e publish:** o evento continua pendente e qualquer relay publica.
- **Crash entre publish e confirm:** o lease expira e outro relay republica **com o mesmo `eventId`**.
- **Destino:** SNS `wager-events.fifo`, com `MessageGroupId = walletId`,
  `MessageDeduplicationId = eventId` e atributos `eventType`/`eventVersion` (para filter policies),
  fanout raw para `wager-events-audit.fifo` (assinante de exemplo, com DLQ). Consumidores devem
  deduplicar por `eventId` e podem ordenar por `walletVersion`.

**Envelope:**

```json
{"eventId":"…","eventType":"WalletBalanceChanged","aggregateId":"<walletId>","correlationId":"…",
 "causationId":"…","occurredAt":"2026-09-08T12:00:00.000Z","version":1,"data":{…}}
```

| Evento | Gatilho | `data` |
|---|---|---|
| `WagerTransactionProcessed` | conclusão (inclui LOSS e OPENING) | transação + `balance` + `walletVersion` |
| `WagerTransactionRejected` | rejeição definitiva | transação + `failureCode` |
| `WagerTransactionPendingReference` | primeira espera por referência | transação + `nextAttemptAt` + `referenceDeadline` |
| `WalletBalanceChanged` | mudança efetiva de saldo | `walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion` |

Tipo e versão são fixados no construtor; timestamps em UTC RFC 3339; dinheiro em strings decimais.

## Autenticação e autorização

**IdP: Keycloak 26.8** (recomendado pelo desafio). Padrão OIDC, `client_credentials`, provisionado
por Terraform (provider oficial `keycloak/keycloak`). Não há cadastro de senhas nem emissão própria de
tokens.

**Validação:**
- assinatura RS256 com JWKS em cache e renovação quando aparece um `kid` desconhecido (`go-oidc`);
- `iss` igual à URL **pública** do realm, mesmo com o JWKS buscado no endereço **interno**
  (`keycloak:8080`), o que torna válidos os tokens obtidos por dentro ou por fora;
- `aud` contém `jungle-api` (mapper de audience);
- expiração e `typ=Bearer`.

**Permissões (scopes por client):**

| Client | Scopes | `provider_id` |
|---|---|---|
| `provider-a`, `provider-b` | `wagering.write`, `wagering.read` | claim fixo, que define o provider autorizado |
| `jungle-internal` | `wallets.write`, `wallets.read`, `wallets.reconcile`, `wagering.read` | — |
| `provider-c-shortlived` | (provider) | token de 5 s, para testes de expiração |

**Regras:**
- operações de carteira só com scopes internos;
- o submit exige identidade de provider e `providerId` do corpo igual ao claim (senão
  `403 PROVIDER_MISMATCH`, sem efeito);
- leituras e replays de transação de outro provider → `404` (não revela existência).

**Broker:** um principal IAM por componente, com least privilege aplicado pelo MiniStack
(`AUTH=true`):
- `jungle-consumer`: receive/delete/visibility na fila; send só na DLQ;
- `jungle-outbox-publisher`: publish no tópico;
- `jungle-producer-<provider>`: send na fila;
- `jungle-events-reader`: lê a fila de auditoria.

O consumer mantém todas as validações de domínio.

## Uber Fx, ciclo de vida e shutdown

- Módulos: `platform`, `postgres`, `messaging`, `metrics`, `app`, `http` e as roles `resolver`,
  `consumer`, `outbox`, `reconciler` (`ROLES`), com injeção por construtores (`fx.Provide`/
  `fx.Invoke`, grupos para os health checks).
- **Início:**
  - configuração validada antes do Fx (`caarlos0/env`, segredos via `*_FILE`);
  - pool com espera ativa pelo banco dentro do `StartTimeout`;
  - workers com contexto próprio;
  - URLs do SQS resolvidas com retry, então um SQS indisponível não derruba o processo.
- **Parada** (ordem inversa do início):
  1. workers cancelados; o término é observável e uma transação em voo faz rollback;
  2. consumer para de buscar e termina ou libera o que está em andamento;
  3. HTTP: readiness vai a 503, espera `DRAIN_DELAY` para o edge tirar a instância, depois
     `Shutdown`;
  4. pool fechado por último.

  `StopTimeout` 25 s < `stop_grace_period` 30 s.
- **Testes:** `fx.ValidateApp` com todas as roles e start/stop completo com dependências reais e
  `goleak`.

## Observabilidade

- **Logs JSON** com **allowlist de atributos**: só identificadores (`correlationId`, `messageId`,
  `transactionId`, `walletId`, `providerId`, …) e fatos operacionais. Valores, saldos, corpos e
  credenciais são descartados mesmo se alguém os logar.
- **Métricas Prometheus** (`:9090`, não roteada pelo edge): resultados por canal/tipo/status/replay,
  latência, conflitos de concorrência, mensagens SQS por resultado/duplicata, DLQ, tentativas de
  referência, outbox publicada/falha/estacionada, **atraso da outbox**, pendências, reconciliações,
  HTTP.
- **Alertas:** divergência, DLQ, atraso da outbox, evento estacionado, pendências.
- **Dashboard** Grafana provisionado (`make obs-up`).
- **Reconciliação:** `POST /wallets/{id}/reconciliation` e sweep periódico (role `reconciler`).

## Infraestrutura

- **Compose** (projeto `jungle`, portas só em 127.0.0.1): Postgres 17, Keycloak 26.8 (`/auth`),
  MiniStack 1.5.19, provisioner (Terraform), migrate, `api-1..3`, Traefik; profiles `obs` e `edge`.
- **Terraform** em container, dentro do compose: um `docker compose up --build` provisiona tudo, sem
  Terraform no host.
  - `stacks/platform`: realm/clients/scopes/mappers, filas/tópico/assinatura/IAM, papéis do
    Postgres; segredos gerados (`random_password`) e entregues em volume.
  - `stacks/edge-lab`: rota pública no Caddy da VM, com state próprio.
- **Por que MiniStack:** o LocalStack exige auth token desde 2026-03-23, o que quebra a reprodução a
  partir de checkout limpo. O MiniStack é MIT e aplica IAM.
  - Achado: o MiniStack exige `sns:PublishBatch` explicitamente (a AWS usa `sns:Publish`), por isso
    a policy lista os dois.
  - Plano B: LocalStack 4.9.2 fixado.

## Limitações e trabalho não concluído

- **`SenderId` no MiniStack é a conta:** o binding remetente → provider só é estrito na AWS real
  (mapa pronto, gerado pelo Terraform).
- **Throughput da outbox no ambiente local** é limitado pelo SNS emulado e pela ordem estrita por
  carteira (ver [docs/LOADTEST.md](docs/LOADTEST.md)). Melhoria proposta: claim de vários eventos por
  partição com advisory lock.
- **Partidas dobradas e tracing (OpenTelemetry)** não foram implementados (diferenciais opcionais).
- **Superusuário** consegue burlar triggers (limitação inerente; mitigada por papéis).
- **Persistência do MiniStack** só é salva num shutdown gracioso. Um crash do emulador perde filas,
  mensagens e IAM. Isso aconteceu uma vez durante os testes de carga: a fila de auditoria acumulou
  eventos e o container estourou o limite de memória. Mitigação: retenção de 1 h na fila de
  auditoria, limite de memória maior e `make recover-broker`, em que o Terraform recria e as APIs
  releem as credenciais. Nenhum dado financeiro se perde: os eventos esperam na outbox e o relay
  republica.
- **Interpretações adotadas:**
  - identificadores restritos a `[A-Za-z0-9._:-]{1,128}`;
  - BET e LOSS não aceitam referência;
  - WIN com referência espera a BET;
  - reversão única por transação, qualquer que seja o tipo;
  - moedas BRL, USD, EUR.
