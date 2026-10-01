# Teste de carga

## Como reproduzir

```bash
make up && make obs-up                         # stack + Prometheus (lag da outbox)
make load-test                                  # 20 VUs, 60 s, 50 carteiras
make load-test VUS=3 WALLETS=200 DURATION=90s   # variações
```

O k6 (`grafana/k6:2.3.0`) roda num container na rede `jungle_net` ([test/load/wagering.js](../test/load/wagering.js)).
O setup obtém tokens no Keycloak real e abre carteiras pelo edge. As submissões vão em round-robin
direto para `api-1..3`, porque o rate limit do edge (200 req/s por origem) seria o que se mediria
com uma origem só. Cada iteração faz uma `BET` de 1.00 numa carteira aleatória; 10% das iterações
são **replays** da iteração anterior do mesmo VU (mesma chave e payload), exercitando a idempotência.
O atraso da outbox vem do Prometheus (`jungle_outbox_oldest_pending_seconds`).

## Ambiente

- VM Hostinger com **2 vCPU** AMD EPYC 9354P e 8 GB RAM, dividida com outros containers da VM.
- Stack completo na mesma máquina: Postgres 17, Keycloak, MiniStack, Traefik, 3 instâncias da API
  (todas com consumer, resolver e outbox ativos), Prometheus e Grafana. O k6 também roda na mesma VM.

## Resultados (2026-10-01)

| Perfil | Req/s | p50 | p95 | p99 | Erros | Replays | Rejeições | Conflitos de concorrência |
|---|---|---|---|---|---|---|---|---|
| 20 VUs, 50 carteiras, 60 s | **232** | 74.7 ms | 190 ms | 274 ms | 0 | 1 427 | 0 | 0 |
| 20 VUs, 1 000 carteiras, 60 s | 223 | 81.3 ms | 174 ms | 234 ms | 0 | 1 323 | 0 | 0 |
| 3 VUs, 200 carteiras, 90 s | 212 | **12.3 ms** | 29.8 ms | 43.9 ms | 0 | — | 0 | 0 |

**Leitura:** a vazão máxima nesta VM fica em ~220 transações/s e não depende do número de VUs. Com 3
VUs ela já é atingida, com latência baixa; com 20 VUs só cresce a fila (latência). Durante a carga,
o **Postgres consome ~105% de CPU** (das 2 vCPUs), enquanto as 3 APIs ficam em ~11% cada e o
MiniStack em ~12%. O limite é o banco nesta máquina pequena e compartilhada. Cada transação faz
INSERT, lock e UPDATE da carteira, INSERT no ledger, UPDATE do status, 1–2 INSERTs na outbox e os
triggers de invariantes, inclusive os diferidos.

## Atraso da outbox

| Perfil | Lag máximo | Publicação |
|---|---|---|
| 3 VUs, 200 carteiras (sem backlog inicial) | 87 s | ~60 eventos/s em média |
| 20 VUs, 50 carteiras | crescente durante o teste | pico ~93 eventos/s |

A ~220 transações/s são gerados ~440 eventos/s, e o relay não acompanha neste ambiente. O atraso
cresce durante o burst e **drena sozinho depois** (≈5 min para ~13 mil transações). Nenhum evento é
perdido (outbox durável) e a ordem por carteira é mantida.

Medição isolada de uma rodada do relay, com os relays de produção parados e backlog presente:

| Etapa | Tempo | Observação |
|---|---|---|
| claim | ~130 ms | 75 eventos: um por carteira com pendência |
| publish | ~600 ms | 75 eventos em 8 `PublishBatch` paralelos no MiniStack |
| confirm | ~5 ms | `UPDATE` único, cercado por `claim_id` |

Gargalos, por ordem de impacto:
1. **SNS FIFO emulado** (MiniStack, Python): ~100–130 mensagens/s por relay. Na AWS real, um tópico
   SNS FIFO aceita ordens de grandeza a mais com batching.
2. **Ordem estrita por carteira**: cada rodada publica no máximo **um evento por carteira** (a cabeça
   da partição). É a escolha consciente de ordem acima de disponibilidade. Carteiras muito quentes
   publicam sequencialmente.
3. CPU do Postgres disputada com as submissões.

Melhorias aplicadas por causa deste teste:
- claim com `NOT EXISTS` e índices parciais sobre linhas não publicadas, no lugar de `DISTINCT ON`
  sobre todo o pendente (migration 00006);
- confirmação em lote;
- `PublishBatch` concorrente;
- lote de 200.

Resultado: a publicação subiu de ~30 para ~80–90 eventos/s.

Próximos passos (não feitos):
- claim de vários eventos consecutivos por carteira numa rodada, com advisory lock por partição e
  publicação ordenada dentro do mesmo batch;
- histograma por etapa do relay;
- banco e emulador em máquina dedicada.

## Integridade após a carga

Depois de cada execução: reconciliação sem divergências (`jungle_reconciliations_total{result="divergent"} = 0`),
nenhum conflito de concorrência e zero respostas fora de 200.
