# Changelog

## 1.0.0 (2026-10-02)


### Features

* **cd:** lab watchdog heals MiniStack state loss; dependabot ignores the pinned Caddy CLI ([ab4a6c9](https://github.com/fredzolio/backend-go-jungle/commit/ab4a6c9bf1f8697e30b54910fd256467dc67d579))
* F0 foundation — isolated compose env, Terraform control plane, Fx skeleton ([b9b6d5f](https://github.com/fredzolio/backend-go-jungle/commit/b9b6d5f2f92d72e0055aca013fe40c83939695ea))
* F1 pure domain — money, wallet, wagering rules, events ([6a7bdba](https://github.com/fredzolio/backend-go-jungle/commit/6a7bdbaf32b40c0c6e01297ecfb11b8b7e8c502c))
* F10 public lab exposure at jungle.lab.fredzol.io ([c620e12](https://github.com/fredzolio/backend-go-jungle/commit/c620e1284cb60b42b5dbe38c9de145904439e3f3))
* F2 persistence — migrations, DB invariants, pgx stores, integration tests ([6a1d501](https://github.com/fredzolio/backend-go-jungle/commit/6a1d501920b009aa8d028f4ac14492d88af7e421))
* F3 core use cases — open wallet, synchronous idempotent submit ([5261897](https://github.com/fredzolio/backend-go-jungle/commit/5261897ec03685944a886b5b07ac6bdb3abde8d9))
* F4 reversals and pending-reference resolver ([a89a971](https://github.com/fredzolio/backend-go-jungle/commit/a89a9714dd6dd4717ecd01a8286cb5aacd2e73bd))
* F5 HTTP API with OIDC authN/Z and OpenAPI ([41e2f2b](https://github.com/fredzolio/backend-go-jungle/commit/41e2f2b76891464e40ece7de23a2164cf28e5bef))
* F6 SQS consumer with transactional inbox and DLQ ([5f639c9](https://github.com/fredzolio/backend-go-jungle/commit/5f639c93aeb515bdefa61f846981a179415cffc1))
* F7 transactional outbox relay to SNS FIFO ([e3b6363](https://github.com/fredzolio/backend-go-jungle/commit/e3b63631b37193a947d75ee6bb61a3c091a2225f))
* F8 observability and reconciliation ([4c59eb0](https://github.com/fredzolio/backend-go-jungle/commit/4c59eb095de3a44c181a4bdf12cdfcfa0a746552))
* F9 multi-process and failure scenarios ([4f589af](https://github.com/fredzolio/backend-go-jungle/commit/4f589af8fdc1b1e6613004bc22a4fd52c75443bb))
* **idp:** evaluation clients with a published secret (demo-internal, demo-provider-1/2) ([0e40639](https://github.com/fredzolio/backend-go-jungle/commit/0e40639adff97d1d2ddd35d60e94f5deebb380aa))


### Bug Fixes

* **cd:** lab-* targets always operate on the deploy checkout ([3e1f5c5](https://github.com/fredzolio/backend-go-jungle/commit/3e1f5c5e8bed3ba61f2d3fc01832a661532306cd))
* **cd:** pass lab environment secrets to deploy-lab; derive immutable OIDC subject ([c2b37a0](https://github.com/fredzolio/backend-go-jungle/commit/c2b37a0511536718ac69691d937427ab50830a74))


### CI/CD

* quality and security gates (lint, unit, integration, system, e2e, security, nightly) ([64987c8](https://github.com/fredzolio/backend-go-jungle/commit/64987c83066ac50ad396eda364c5c2f3b5ae4d2a))


### Documentation

* CI/CD pipeline and lab deploy (docs/CICD.md, README, ARCHITECTURE) ([7c1bd65](https://github.com/fredzolio/backend-go-jungle/commit/7c1bd65914bbfa79a7ee8ac96bbf4427fa61dc58))
* F11 delivery docs, load test and outbox throughput work ([a9fefc4](https://github.com/fredzolio/backend-go-jungle/commit/a9fefc4f39c8a7faac1a143b383c18c30cb2e858))
* public lab walkthrough with evaluation credentials ([12c85ad](https://github.com/fredzolio/backend-go-jungle/commit/12c85adc11fbe6199b6de270c966cfb51bd0732a))
