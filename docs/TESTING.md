# Testes

Nenhuma suíte substitui PostgreSQL, SQS ou o IdP por mocks: integração e sistema usam containers
reais (testcontainers) e o e2e usa o stack completo com o Keycloak real.

| Suíte | Build tag | Comando | Pré-requisito | O que cobre |
|---|---|---|---|---|
| Unitários | — | `make test` / `make test-race` | Go 1.27.1 | domínio (Money + fuzz, Wallet, máquina de estados, regras), hash golden, logger, grafo Fx, guardas de arquitetura |
| Integração | `integration` | `make test-integration` | Docker | adapters × Postgres/MiniStack reais, casos de uso, HTTP com JWT real, consumer, relay, ciclo de vida do Fx |
| Sistema | `system` | `make test-system` | Docker + gcc (`-race`) | binário real (`-race`, `-tags faultinject`) como **processos independentes**, morte em pontos exatos |
| E2E | `e2e` | `make up && make test-e2e` | stack no ar | tokens do Keycloak real, SQS com IAM aplicado (`AUTH=true`), eventos na fila assinante, pelo edge |
| E2E público | `e2e` | `make lab-smoke` (na VM) | rota publicada | a mesma suíte e2e por `https://jungle.lab.fredzol.io` |
| Carga | — | `make load-test` | stack no ar | ver [LOADTEST.md](LOADTEST.md) |

`go vet` deve passar com todas as tags: `for t in "" integration e2e system faultinject; do go vet -tags="$t" ./...; done`.

## Cenários obrigatórios do desafio → teste

| # | Cenário | Teste(s) |
|---|---|---|
| 1 | Mesma aposta 50× em paralelo → um débito | `app: TestSameBet_50_times_in_parallel_debits_exactly_once` · `system: TestThreeProcesses_same_bet_50_times_debits_once` |
| 2 | Duas apostas de 80.00 sobre 100.00 | `app: TestTwo_bets_of_80_on_100_one_processed_one_rejected` (10 rodadas + reenvios) · `system: TestThreeProcesses_two_bets_of_80_contend_and_one_wins` (contenção entre processos provada por `pg_stat_activity`) · `e2e: TestRealIdP_end_to_end_flow_with_provider_isolation` |
| 3 | Carteiras distintas em paralelo | `app: TestWallets_are_processed_independently` (B processa com A travada) · `app: TestMany_wallets_concurrently_stay_consistent` · `system: TestThreeProcesses_distinct_wallets_in_parallel` |
| 4 | ≥ 3 instâncias independentes | toda a suíte `system` (3 processos de SO, pools próprios) |
| 5 | Consumer interrompido após commit e antes do delete | `sqsconsumer: TestCrash_after_commit_before_delete_is_redelivered_safely` · `system: TestConsumer_process_killed_after_commit_before_delete` (`consumer.after_commit`) |
| 6 | Dois publishers disputando a outbox + recuperação | `snspublisher: TestTwo_relays_contending_publish_everything_in_order` · `TestRelay_publishes_committed_events_once_in_wallet_order` (crash entre commit e publicação) · `TestCrash_between_publish_and_confirm_republishes_same_event_id` · `system: TestRelay_process_killed_between_publish_and_confirm` (`relay.after_publish`) |
| 7 | REFUND/ROLLBACK antes da referência | `app: TestRefund_before_its_bet_waits_and_resolves_after_the_bet` · `TestReversal_without_reference_is_rejected_after_ttl` · `TestConcurrent_resolvers_conclude_each_pending_once` · `system: TestPending_reference_survives_the_process_that_accepted_it` |
| 8 | Reinício preserva idempotência, pendências e consistência | `system: TestFull_restart_preserves_idempotency_pending_and_consistency` · `system: TestPending_reference_survives_the_process_that_accepted_it` |
| — | Cruzamento HTTP × SQS | `sqsconsumer: TestSame_operation_via_HTTP_and_SQS_is_applied_once` · `system: TestHTTP_and_SQS_copies_of_an_operation_apply_once_across_processes` · `e2e: TestSQS_operation_reaches_the_wallet_and_matches_HTTP_idempotency` |
| — | Saldo × ledger ao final | `system: assertLedgerMatchesBalances` (todas as carteiras, ao fim de cada cenário) · reconciliação (`app: TestReconcile_*`) |

## Outras verificações exigidas

| Exigência | Teste(s) |
|---|---|
| Money: parsing, escala, limites, inválidos, moedas | `money_test.go` (tabelas + `FuzzParse`) |
| Invariantes da carteira, transições, 5 tipos, zero, OPENING | `wallet_test.go`, `transaction_test.go`, `rules_test.go`, `request_test.go`, `events_test.go` |
| Conflito de payload para a mesma chave | `request_test.go: TestPayloadHash_ignores_idempotency_key_but_not_business_fields` · `app: TestIdempotency_conflicts` · HTTP 409 |
| Migrations, constraints, imutabilidade do ledger | `postgres: TestMigrations_apply_revert_and_reapply`, `TestWallet_invariants…`, `TestLedger_is_append_only_for_owner_and_app`, `TestLedger_entry_must_match_its_transaction`, `TestLedger_chain_breaks_are_rejected`, `TestWallet_must_match_its_ledger_at_commit`, `TestTransactions_*` |
| Inbox, reentrega, DLQ, retry | suíte `sqsconsumer` (inclui ordem FIFO após falha transitória) |
| Composição Fx, início/encerramento, liberação dos workers | `bootstrap: TestGraph_with_all_roles_is_complete` · `TestApplication_starts_serves_and_stops_without_leaks` (`goleak`) · `system`: encerramento limpo no SIGTERM |
| IdP real: ausente, inválido, expirado | `e2e: TestRealIdP_rejects_missing_tampered_and_expired_tokens` · `httpapi: TestAuthentication_rejects_missing_invalid_and_expired_tokens` |
| Isolamento entre providers (consultas e replays), operações internas | `httpapi: TestProviders_are_isolated_on_reads_and_replays`, `TestAuthorization_wallet_operations_are_internal_only` · e2e |
| Sem efeito financeiro em acesso não autorizado | `app: TestSubmit_correctable_errors_persist_nothing` · `httpapi: TestSubmit_contract_status_codes` (saldo inalterado) |
| Sem float | `archtest: TestNoFloatIdentifiersOutsideAllowlist` |
| Domínio independente de Fx/HTTP/SQS/pgx | `archtest: TestDomainImportsOnlyStdlibUUIDAndDomain` |

## Simulação de falhas

- **Morte de processo em ponto exato:** compile com `-tags faultinject` e defina
  `JUNGLE_FAULTS=consumer.after_commit|relay.after_publish|resolver.before_commit`. O processo sai
  com 137 naquele ponto, como um SIGKILL. No binário de produção os pontos são no-op.
- **Banco travado / lock timeout:** os testes seguram o lock da carteira numa transação paralela
  (`lock_timeout` curto) e provam o erro transitório (503 / retry com backoff).
- **Broker recusando:** publisher que falha → backoff, depois `dead_at` (`TestFailing_publications_back_off_and_park_the_head`).
- **Remetente SQS não autorizado e mensagens veneno** → DLQ com `errorCode`.

Para mutações de controle, inverta a regra e veja o teste falhar. Isso foi feito durante o
desenvolvimento com: índice de reversão com `kind`, direção do ROLLBACK, remoção do
`FOR NO KEY UPDATE`, remoção de um provider do Fx.
