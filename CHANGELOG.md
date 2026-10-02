# Changelog

## 1.0.0 (2026-10-02)


### Features

* **cd:** lab watchdog heals MiniStack state loss; dependabot ignores the pinned Caddy CLI ([f01f6c8](https://github.com/fredzolio/backend-go-jungle/commit/f01f6c8715e46a67fe69effc099a5cee11eb8f7e))
* F0 foundation — isolated compose env, Terraform control plane, Fx skeleton ([244fc8a](https://github.com/fredzolio/backend-go-jungle/commit/244fc8a2c752664ec963e44767ee60fab634edea))
* F1 pure domain — money, wallet, wagering rules, events ([9da14b0](https://github.com/fredzolio/backend-go-jungle/commit/9da14b0e9c9e3b35ec4d71473d8c106d950d39ca))
* F10 public lab exposure at jungle.lab.fredzol.io ([b774936](https://github.com/fredzolio/backend-go-jungle/commit/b7749360299f27c577d602f7799a87a00f4d2387))
* F2 persistence — migrations, DB invariants, pgx stores, integration tests ([7a58d74](https://github.com/fredzolio/backend-go-jungle/commit/7a58d7446d7b48c8343f136781b016f2a346755b))
* F3 core use cases — open wallet, synchronous idempotent submit ([b9315e8](https://github.com/fredzolio/backend-go-jungle/commit/b9315e812b2a2ac3e7b4016fd76d032d2957d8d2))
* F4 reversals and pending-reference resolver ([653ac4e](https://github.com/fredzolio/backend-go-jungle/commit/653ac4ebb10f693d874d47d59cebe9dea0078828))
* F5 HTTP API with OIDC authN/Z and OpenAPI ([8d1f641](https://github.com/fredzolio/backend-go-jungle/commit/8d1f641a9cc2c2a4766253b984c762821a98b45b))
* F6 SQS consumer with transactional inbox and DLQ ([b949649](https://github.com/fredzolio/backend-go-jungle/commit/b9496496ecfcfa5add40f44cf9324138d8baa8e0))
* F7 transactional outbox relay to SNS FIFO ([26a3aa1](https://github.com/fredzolio/backend-go-jungle/commit/26a3aa1c54736369d00c6139408e8f024b10dfb5))
* F8 observability and reconciliation ([77c816f](https://github.com/fredzolio/backend-go-jungle/commit/77c816f344c1e9b76ea22d48e1205bb1b35617ce))
* F9 multi-process and failure scenarios ([2503c93](https://github.com/fredzolio/backend-go-jungle/commit/2503c937cd1139cf1565404105f282544d246db1))
* **idp:** evaluation clients with a published secret (demo-internal, demo-provider-1/2) ([3c19d47](https://github.com/fredzolio/backend-go-jungle/commit/3c19d4718ff95a7b8c839694a0a56a3f35c93012))


### Bug Fixes

* **cd:** lab-* targets always operate on the deploy checkout ([758268f](https://github.com/fredzolio/backend-go-jungle/commit/758268f0b38ec97b712f13e519ac85d5679569d3))
* **cd:** pass lab environment secrets to deploy-lab; derive immutable OIDC subject ([22aee1d](https://github.com/fredzolio/backend-go-jungle/commit/22aee1d476f5b86e4b5a84ec362ccce368572e7b))


### CI/CD

* quality and security gates (lint, unit, integration, system, e2e, security, nightly) ([79cbc4e](https://github.com/fredzolio/backend-go-jungle/commit/79cbc4e68ce7e9aef089b9a0e93dba134c3f3b5f))


### Documentation

* CI/CD pipeline and lab deploy (docs/CICD.md, README, ARCHITECTURE, CHECKPOINT F12) ([18c5fa0](https://github.com/fredzolio/backend-go-jungle/commit/18c5fa0a226578f2967cc3c166437b634fc0a131))
* F11 delivery docs, load test and outbox throughput work ([7fec020](https://github.com/fredzolio/backend-go-jungle/commit/7fec02035543b2c739b6a1ea6714e286173c6773))
* public lab walkthrough with evaluation credentials ([dbec6c1](https://github.com/fredzolio/backend-go-jungle/commit/dbec6c1f2cf2f4bd3d13b3d1a08677a41d443b39))
