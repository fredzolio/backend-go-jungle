# CI/CD

Pipeline em GitHub Actions. Todo PR passa pelos mesmos gates que a `main`; um push na `main` que
passa em tudo publica uma imagem assinada e faz o deploy no lab (`https://jungle.lab.fredzol.io`)
com rolling update, smoke e2e e rollback automático.

## Fluxo

```
PR / push main ──► changes ─┬─► lint ─────────┐
                            ├─► unit ─────────┤
                            ├─► integration ──┤
                            ├─► system ───────┤──► image ──► deploy-lab (só main)
                            ├─► e2e ──────────┤      │
                            ├─► infra-lint ───┤      ▼
                            └─► security ─────┘    ci-ok  (check obrigatório da main)
```

| Job | O que garante |
|---|---|
| `lint` | `gofmt`, `go vet` em todos os conjuntos de tags, `go mod tidy -diff`, `go mod verify`, golangci-lint v2 ([.golangci.yml](../.golangci.yml)) |
| `unit` | `-race -shuffle=on`, cobertura no resumo do job |
| `integration` | Postgres e MiniStack reais (testcontainers) |
| `system` | os 8 cenários obrigatórios com 3+ processos reais, `-race` e pontos de falha |
| `e2e` | o stack completo do `compose.yaml` sobe no runner (Keycloak, MiniStack, Terraform, 3 APIs, Traefik) e roda `make test-e2e` |
| `infra-lint` | `terraform fmt/validate` dos dois stacks, hadolint, actionlint + shellcheck, `docker compose config`, Spectral no OpenAPI |
| `security-quick` | govulncheck, gitleaks (histórico completo), Trivy (dependências + misconfig de Dockerfile/compose/Terraform) |
| `dependency-review` | bloqueia PR que introduz dependência com vulnerabilidade alta |
| `image` | build, gate do Trivy na imagem (HIGH/CRITICAL corrigíveis). Na `main`: push no GHCR com SBOM e proveniência SLSA, attestation e assinatura cosign keyless |
| `deploy-lab` | deploy no lab (abaixo); só na `main` e com `LAB_DEPLOY_ENABLED=true` |

Em PR que só muda documentação, os jobs pesados são pulados (`dorny/paths-filter`); `ci-ok` aceita
"skipped" e falha só com "failure"/"cancelled". A imagem só é publicada depois de todos os gates,
então as tags `main` e `sha-*` nunca apontam para um commit vermelho.

Outros workflows:
- **codeql** e **scorecard** (OpenSSF): semanal e a cada push na `main`, resultados na aba Security.
- **nightly** (03:00 UTC): cenários de sistema com `-count=3` (caça a flakes), teste de carga k6
  com thresholds de regressão em [test/load/wagering.js](../test/load/wagering.js) e nova varredura
  de CVEs. Se falhar, abre ou atualiza uma issue `nightly-failure`.
- **release-please**: mantém um PR de release a partir dos Conventional Commits. Ao publicar a
  release, promove a imagem já construída daquele commit para `vX.Y.Z` e `latest` (mesmo digest,
  sem rebuild) e confere a assinatura.
- **Dependabot**: Go, Actions, imagens Docker e providers Terraform, semanal e agrupado.

Todas as actions estão fixadas por SHA de commit. Repositório público: secret scanning com push
protection, alertas do Dependabot e reporte privado de vulnerabilidades ativos. Ruleset da `main`:
sem force-push nem deleção, `ci-ok` obrigatório em PR (admin tem bypass para push direto, que
passa pelo mesmo pipeline antes do deploy).

## Deploy no lab

```
runner GitHub ──OIDC──► Tailscale (tag:ci, efêmero) ──tailnet──► VM 100.86.214.26:2222
                                                                 sshd sem root, chave com forced command
                                                                 └► ssh-entry → deploy.sh
```

1. **Rede sem segredo de longa duração.** O runner entra na tailnet por *workload identity
   federation*: o token OIDC do GitHub é trocado por uma credencial Tailscale efêmera com
   `tag:ci`. A identidade federada aceita só o subject
   `repo:fredzolio@91195110/backend-go-jungle@1400476308:environment:lab` (formato imutável do
   GitHub: sobrevive a renomeação e não é reaproveitável por outro repo com o mesmo nome).
2. **A tailnet limita o alcance.** A policy dá a `tag:ci` acesso só a `100.86.214.26:2222`; o
   allow-all ficou restrito aos membros. Testes da própria policy travam isso.
3. **A VM limita o que o CI pode fazer.** Na porta 2222 escuta um sshd sem root, só no IP da
   tailnet (o Tailscale SSH já ocupa a 22; a 2222 também é bloqueada pelo UFW na interface
   pública). A chave do CI tem `restrict`, `from="100.64.0.0/10,…"` e um forced command
   ([ssh-entry.sh](../deploy/lab/ssh-entry.sh)) que aceita só `deploy <sha> <imagem@digest>`,
   `rollback` e `status`.
4. **A VM não confia no pipeline.** Antes de qualquer mudança, o `ssh-entry` exige que o commit
   seja ancestral de `origin/main` e que a imagem tenha assinatura cosign emitida para um workflow
   deste repositório em `refs/heads/main`. Não existe flag para pular essas checagens. O token do
   GHCR chega por stdin e vive num `DOCKER_CONFIG` temporário.
5. **Rollout** ([deploy.sh](../deploy/lab/deploy.sh)):
   1. decifra o env com SOPS e marca a imagem atual como âncora de rollback;
   2. garante a infraestrutura (Postgres, Keycloak, MiniStack, edge) e roda o provisioner
      Terraform, que também recria filas e IAM se o MiniStack tiver perdido estado;
   3. aplica as migrations com a imagem nova (política expand/contract: compatíveis com a versão
      anterior e nunca revertidas);
   4. troca `api-1`, `api-2` e `api-3` uma por vez, esperando o Traefik ver cada uma saudável;
   5. confere o digest em execução e o readiness local e público;
   6. roda a suíte e2e contra a URL pública, em container.
   Qualquer falha a partir das migrations volta as três APIs para a âncora e termina com
   `DEPLOY_RESULT=rolled_back`.
6. **Verificação externa.** O runner checa pela internet: `/health/ready`, `/docs` e
   `/openapi.yaml` respondem 200, `/auth/admin/` não é público e negócio sem token dá 401.

O deploy nunca mexe no Caddy da VM (o Caddy 2.6.2 da VM entra em panic em reload). Se o stack
`edge-lab` mudar, o deploy avisa para rodar `make lab-expose` à mão. Deploys são serializados
(`concurrency` no GitHub e `flock` na VM) e cada chamada fica no audit log
(`~/.local/state/jungle-deploy/audit.log`).

Disponibilidade medida com sondas a cada 0,5 s no HTTPS público: durante a troca das APIs aparece
no máximo um 503 isolado por deploy. É a instância que está drenando respondendo 503 no readiness
antes de o health check do Traefik (2 s) tirá-la do balanceamento.

### Operação

| Ação | Como |
|---|---|
| Deploy de um commit da `main` já publicado | Actions → `deploy-lab` → *Run workflow* com o SHA |
| Pausar deploys automáticos | variável de repositório `LAB_DEPLOY_ENABLED` ≠ `true` |
| Rollback para a release anterior | `make lab-rollback` na VM |
| Estado (release atual/anterior, imagens) | `make lab-deploy-status` na VM |
| Deploy manual na VM (inclusive imagem local) | `make lab-deploy IMAGE=<ref> SHA=<sha>` |
| Reinstalar o canal de deploy (sshd, forced command, cosign) | `make lab-deploy-bootstrap` |
| Editar variáveis do lab | `make lab-env-edit` (SOPS) |

### Segredos e configuração

- **Env do lab**: [deploy/lab/lab.enc.env](../deploy/lab/lab.enc.env), cifrado com SOPS + age para
  dois destinatários (regras em [.sops.yaml](../.sops.yaml)): a chave da VM, que nunca sai dela e
  decifra no deploy, e a chave do cofre zoliolab, para edição. Os segredos dos serviços continuam
  sendo gerados pelo Terraform dentro do stack.
- **Environment `lab` no GitHub** (só a `main` pode usar): segredo `LAB_SSH_PRIVATE_KEY`; variáveis
  `LAB_SSH_HOST`, `LAB_SSH_PORT`, `LAB_SSH_USER`, `LAB_SSH_KNOWN_HOSTS`, `LAB_PUBLIC_URL`,
  `TS_OAUTH_CLIENT_ID`, `TS_AUDIENCE`.
- **Tailscale**: [tailscale-bootstrap.sh](../deploy/lab/tailscale-bootstrap.sh) reproduz a policy e
  a identidade federada. É idempotente, mostra o diff por padrão e só altera com `--apply`
  (precisa de um access token temporário em `TS_API_KEY`).
