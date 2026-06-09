# AGENTS.md — Dispatch

> Extraído da auditoria de 2026-06-08. Baseline: `e6227c38d4d3eba91988466860211ecc0932a6b8`.
> Convenções refletem o código existente, não aspirações.

---

## What is this project?

Dispatch é um serviço de entrega de webhooks em Go. Recebe eventos via HTTP API,
publica em Kafka, e workers consomem e entregam para endpoints registrados (subscriptions).
Tem retry com exponential backoff, rate limiting e circuit breaker por subscription (Redis ou in-memory).
Estado atual: funcional, build e testes passam, cobertura total de 35.6% — camadas de I/O (repository e Kafka producer/consumer) sem testes.

---

## Project structure

```
cmd/
  dispatch/      → Binário da API (HTTP ingestion + subscription management)
  worker/        → Binário do worker (Kafka consumer + retry poller)
  migrate/       → Binário de migração do banco
  producer/      → Utilitário de carga/teste (não é produção)
internal/
  api/           → Handlers HTTP e roteamento (chi)
  config/        → Parsing de variáveis de ambiente
  domain/        → Entidades e state machine (Event, Subscription) — sem dependências externas
  kafka/         → Producer, Consumer, DeliveryHandler, webhook delivery
  observability/ → Métricas Prometheus, health/readiness handlers, middleware de logging
  repository/
    interfaces.go        → Contratos EventRepository e SubscriptionRepository
    postgres/            → Implementações concretas com pgx (event.go, subscription.go, batcher.go)
  resilience/    → Rate limiter e circuit breaker (Redis e in-memory)
  retry/         → Poller de eventos para retry + interface EventProcessor
  clock/         → Abstração de relógio (testabilidade)
docs/
  audit.md       → Auditoria com evidências — leia antes de qualquer implementação
  next-steps.md  → Direções possíveis com estimativa de esforço
  adr/           → 14 ADRs de decisões arquiteturais
  spec.md        → Especificação original do sistema
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
| Spec original | [docs/spec.md](docs/spec.md) | Para entender intenção de uma feature |
| Limitações e backlog | [docs/LIMITATIONS.md](docs/LIMITATIONS.md) | Para avaliar novas features |
| Decisões arquiteturais | [docs/adr/](docs/adr/) | Antes de propor mudanças estruturais |

---

## External dependencies

Para rodar o projeto e os testes completos, as seguintes dependências precisam estar disponíveis:

| Dependência | Produção | Testes | Como subir |
|-------------|----------|--------|------------|
| PostgreSQL | Sim | Sim | `docker compose up postgres` / testcontainers (automático em testes) |
| Redis | Sim (opcional) | Sim | `docker compose up redis` / testcontainers (automático em testes) |
| Kafka | Sim | Não (mocks) | `docker compose -f docker-compose.kafka.yaml up` |

Redis é opcional em produção — o worker faz fallback para in-memory se `REDIS_URL` não estiver configurado.

Testes que requerem Docker (sobem infra via testcontainers):
- `internal/repository/postgres/batcher_test.go`
- `internal/resilience/redis_circuitbreaker_test.go`
- `internal/resilience/redis_ratelimiter_test.go`

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

Arquivos com alta complexidade e cobertura zero ou baixa. Qualquer alteração
requer testes escritos antes da mudança — não depois.

- `internal/repository/postgres/event.go` (338 linhas, **0%**) — toda persistência de eventos. Risco máximo.
- `internal/repository/postgres/subscription.go` (208 linhas, **0%**) — toda persistência de subscriptions. Risco máximo.
- `internal/kafka/consumer.go` (252 linhas, **0%**) — consumer loop inteiro. Bug aqui = perda silenciosa de eventos.
- `internal/kafka/producer.go` (251 linhas, **0%**) — producer. Falha aqui = eventos não entram na fila.
- `internal/api/handler.go` (253 linhas, **35.6%**) — 3 handlers sem teste, 3 parciais. Não adicione handlers sem cobrir os existentes primeiro.

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
