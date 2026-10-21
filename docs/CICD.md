# CI/CD

Pipeline em GitHub Actions. Todo PR passa pelos mesmos gates que a `main`; um push na `main` que
passa em tudo publica no GHCR uma imagem com SBOM, proveniência e assinatura cosign.

## Fluxo

```
PR / push main ──► changes ─┬─► lint ─────────┐
                            ├─► unit ─────────┤
                            ├─► integration ──┤
                            ├─► system ───────┤──► image (publica só na main)
                            ├─► e2e ──────────┤      │
                            ├─► infra-lint ───┤      ▼
                            └─► security ─────┘    ci-ok  (check obrigatório da main)
```

| Job | O que garante |
|---|---|
| `lint` | `gofmt`, `go vet` em todos os conjuntos de tags, `go mod tidy -diff`, `go mod verify`, golangci-lint v2 ([.golangci.yml](../.golangci.yml)) |
| `unit` | `-race -shuffle=on`, cobertura no resumo do job |
| `integration` | Postgres e MiniStack reais (testcontainers) |
| `system` | os 8 cenários de negócio com 3+ processos reais, `-race` e pontos de falha |
| `e2e` | o stack completo do `compose.yaml` sobe no runner (Keycloak, MiniStack, Terraform, 3 APIs, Traefik) e roda `make test-e2e` |
| `infra-lint` | `terraform fmt/validate` do stack `platform`, hadolint, actionlint + shellcheck, `docker compose config`, Spectral no OpenAPI |
| `security-quick` | govulncheck, gitleaks (histórico completo), Trivy (dependências + misconfig de Dockerfile/compose/Terraform) |
| `dependency-review` | bloqueia PR que introduz dependência com vulnerabilidade alta |
| `image` | build, gate do Trivy na imagem (HIGH/CRITICAL corrigíveis). Na `main`: push no GHCR com SBOM e proveniência SLSA, attestation e assinatura cosign keyless |

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
passa pelo mesmo pipeline antes da publicação da imagem).

## Imagem publicada

O workflow `release-image` roda como parte de `ci` e publica `ghcr.io/fredzolio/backend-go-jungle`
com as tags `sha-<12 primeiros caracteres do commit>` e `main`. Quando o `release-please` publica uma
release, a imagem do commit da release recebe `vX.Y.Z` e `latest` por digest, sem rebuild.

Para conferir a assinatura de uma imagem:

```bash
cosign verify ghcr.io/fredzolio/backend-go-jungle:<tag> \
  --certificate-identity-regexp '^https://github\.com/fredzolio/backend-go-jungle/\.github/workflows/.+@refs/heads/main$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Operação

| Ação | Como |
|---|---|
| Construir e escanear a imagem sem publicar | Actions → `release-image` → *Run workflow* (execuções manuais nunca publicam) |
| Refazer a promoção de uma release que falhou | Actions → `release-please` → *Run workflow* com a tag `vX.Y.Z` e o SHA completo do commit |
| Reproduzir os gates localmente | `make ci-local` (gofmt, `go vet` em todas as tags, golangci-lint e testes com `-race`) |
