# AGENTS.md — Dispatch

> Extraído da auditoria de 2026-06-08. Baseline: `e6227c38d4d3eba91988466860211ecc0932a6b8`.
> Convenções refletem o código existente, não aspirações.

---

## What is this project?

Dispatch é um serviço de entrega de webhooks em Go. Recebe eventos via HTTP API,
publica em Kafka, e workers consomem e entregam para endpoints registrados (subscriptions).
Tem retry com exponential backoff, rate limiting e circuit breaker por subscription (Redis ou in-memory).
Estado atual: funcional, build e testes passam, cobertura total de 49.7%.
Validação automatizada agora é em camadas: testes unitários/componentes, integração
com testcontainers (PostgreSQL + Redis) e smoke E2E fino com infraestrutura real.

---

## Project structure

```
cmd/
  dispatch/      → Binário da API (HTTP ingestion + subscription management)
  worker/        → Binário do worker (Kafka consumer + retry poller)
  migrate/       → Binário de migração do banco
  producer/      → Utilitário de carga/teste (não é produção)
internal/
  app/           → Application assembly + E2E harness; read local README before wiring changes
  api/           → Handlers HTTP e roteamento (chi)
  config/        → Parsing de variáveis de ambiente
  domain/        → Entidades e state machine (Event, Subscription) — sem dependências externas
  kafka/         → Producer, Consumer, DeliveryHandler, webhook delivery; critical local README
  observability/ → Métricas Prometheus, health/readiness handlers, middleware de logging
  repository/
    interfaces.go        → Contratos EventRepository e SubscriptionRepository
    postgres/            → Implementações concretas com pgx; critical local README
  resilience/    → Rate limiter e circuit breaker (Redis e in-memory)
  retry/         → Poller de eventos para retry + interface EventProcessor; critical local README
  clock/         → Abstração de relógio (testabilidade)
docs/
  audit.md       → Auditoria com evidências — leia antes de qualquer implementação
  next-steps.md  → Direções possíveis com estimativa de esforço
  learnings/     → Lições técnicas e decisões práticas extraídas da implementação
  exec-plans/
    active/      → Único plano em execução
    queued/      → Planos futuros definidos, aguardando dependências
    done/        → Histórico de planos concluídos
  adr/           → ADRs de decisões arquiteturais
  spec.md        → Contrato vivo de produto e comportamento
  LIMITATIONS.md → Limitações conhecidas e oportunidades de evolução
migrations/      → SQL migrations numeradas
deploy/          → Grafana dashboards
k8s/             → Kubernetes manifests
scripts/
  benchmark/     → Ferramenta de benchmark de throughput
  testserver/    → Servidor HTTP de teste para desenvolvimento
```

---

## Where to find context

| What | Where | When to read |
|------|-------|--------------|
| **Estado atual (comece aqui)** | [PROGRESS.md](PROGRESS.md) | Primeira coisa em toda sessão |
| Auditoria e gaps | [docs/audit.md](docs/audit.md) | Antes de qualquer implementação |
| Plano ativo | [docs/exec-plans/active/](docs/exec-plans/active/) | Para saber o que fazer agora |
| Planos enfileirados | [docs/exec-plans/queued/](docs/exec-plans/queued/) | Para entender a sequência futura; não implementar antes da promoção |
| Lições de implementação | [docs/learnings/](docs/learnings/) | Depois de mudanças relevantes ou para evitar repetir erros |
| Spec viva | [docs/spec.md](docs/spec.md) | Para entender o comportamento atual e os invariantes do produto |
| Limitações e backlog | [docs/LIMITATIONS.md](docs/LIMITATIONS.md) | Para avaliar novas features |
| Decisões arquiteturais | [docs/adr/](docs/adr/) | Antes de propor mudanças estruturais |
| Contexto local de pacote | `internal/{app,kafka,retry,repository/postgres}/README.md` | Antes de alterar um desses subsistemas críticos |

---

## External dependencies

Para rodar o projeto e os testes completos, as seguintes dependências precisam estar disponíveis:

| Dependência | Produção | Testes | Como subir |
|-------------|----------|--------|------------|
| PostgreSQL | Sim | Sim | `docker compose up postgres` / testcontainers (automático em testes) |
| Redis | Sim (opcional) | Sim | `docker compose up redis` / testcontainers (automático em testes) |
| Kafka | Sim | Sim (smoke E2E) | `docker compose -f docker-compose.kafka.yaml up` |

Redis é opcional em produção — o worker faz fallback para in-memory se `REDIS_URL` não estiver configurado.

Testes que requerem Docker (sobem infra via testcontainers):
- `internal/repository/postgres/batcher_test.go`
- `internal/resilience/redis_circuitbreaker_test.go`
- `internal/resilience/redis_ratelimiter_test.go`
- `internal/app/e2e_test.go`

---

## Conventions

Extraídas do código por grep e leitura direta — apenas o que foi verificado:

**Logging:** `log/slog` com `slog.NewJSONHandler(os.Stdout, ...)` — 100% consistente.
Structured fields: `logger.Error("msg", "error", err)` — nunca `fmt.Sprintf` para logging.

**Error handling:** `fmt.Errorf("context: %w", err)` — 83% das ocorrências (25/30).
`errors.New` para erros sem causa (5 casos). Nunca errors.Wrap (sem pkg/errors).

**Context:** `ctx context.Context` como primeiro parâmetro em todas as funções de I/O.
Ausente em helpers/utilitários (correto).

**Interfaces:** definidas no consumidor na maioria dos casos (`EventPublisher` em `api/`,
`EventHandler` em `kafka/`, `EventProcessor` em `retry/`). Exceção: `EventRepository` e
`SubscriptionRepository` ficam em `internal/repository/` — inconsistência menor, não mude sem ADR.

**Functional options:** padrão `WithXxx` consolidado em `kafka.DeliveryHandler`. Siga este
padrão se adicionar configurabilidade a structs complexas.

**Mocks em testes:** definidos localmente no `_test.go` do pacote, não em pacote separado.
Siga este padrão para novos testes.

**Structs retornados, interfaces aceitas:** seguido consistentemente.

---

## Do NOT touch without a test first

Arquivos com acoplamento alto ou cobertura ainda insuficiente. Qualquer alteração
requer teste escrito antes da mudança — não depois.

- `internal/kafka/handler.go` (**57.8%**) — acopla delivery, retry e persistência; o smoke E2E cobre fluxo, não todos os edge cases.
- `internal/observability/middleware.go` / logging middleware (**package 39.1%**) — pouca cobertura e fácil regressão silenciosa.
- `cmd/dispatch/main.go` / `cmd/worker/main.go` (**0%**) — wrappers finos, mas bootstrap final continua sem teste direto.

---

## How to work on this project

### Session start

1. Leia [PROGRESS.md](PROGRESS.md)
2. Execute `go build ./...` — confirme que o baseline compila
3. Leia o exec plan ativo em `docs/exec-plans/active/`
4. Continue do ponto onde o PROGRESS.md indica

### During work

1. Siga os steps do exec plan em ordem
2. Para cada step: escreva o teste primeiro, implemente, verifique
3. Commit após cada step completo
4. Não avance para o próximo step com testes falhando
5. Atualize a spec quando o contrato de comportamento mudar; use ADR para registrar o porquê
6. Ao entrar em um pacote crítico, leia o README local e atualize-o se invariantes ou ownership mudarem

### Package context rules

- README local descreve o mecanismo atual, invariantes, hazards e verificação daquele pacote.
- Não repita roadmap ou decisões extensas: linke para spec, ADR e exec plan.
- Separe explicitamente comportamento implementado de comportamento planejado.
- Se código e README local divergirem, trate como drift: verifique testes e documentos duráveis antes de editar.
- Não crie README para pacotes simples; adicione apenas quando o contexto local reduz risco real de implementação.

### Session end

1. Execute `go build ./...` — confirme que o repo está estável
2. Commit de qualquer trabalho não commitado
3. Atualize [PROGRESS.md](PROGRESS.md) com o que mudou e o que vem a seguir
4. Marque checkboxes do exec plan

---

## Build & verify

```bash
# Build
go build ./...

# Tests (requer Docker para testes de postgres e resilience)
go test ./...

# Layered validation
go test -race ./internal/api/... ./internal/config/... ./internal/domain/... ./internal/kafka/... ./internal/observability/... ./internal/retry/...
go test ./internal/repository/postgres/... ./internal/resilience/...
go test ./internal/app/...

# Tests com race detector
go test -race ./...

# Cobertura por pacote
go test -coverprofile=/tmp/cov.out ./...
go tool cover -func=/tmp/cov.out | grep "total:"

# Docker compose (infra local completa)
docker compose up -d                                # PostgreSQL + Redis
docker compose -f docker-compose.kafka.yaml up -d  # Kafka

# Migrations
go run ./cmd/migrate/...
```
