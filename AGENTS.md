# AGENTS.md — Dispatch

> Extraído da auditoria de 2026-06-08. Baseline: `e6227c38d4d3eba91988466860211ecc0932a6b8`.
> Convenções refletem o código existente, não aspirações.

---

## What is this project?

Dispatch é um serviço de entrega de webhooks em Go. Recebe eventos via HTTP API,
publica em Kafka, e workers consomem e entregam para endpoints registrados (subscriptions).
Tem retry com exponential backoff e limite distribuído de entrega por subscription quando Redis está configurado.
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
  api/           → Handlers HTTP e roteamento (chi); critical local README
  config/        → Parsing de variáveis de ambiente
  domain/        → Entidades e state machine (Event, Subscription) — sem dependências externas
  kafka/         → Producer, Consumer, DeliveryHandler, webhook delivery; critical local README
  observability/ → Métricas Prometheus, health/readiness handlers, middleware de logging
  retention/     → Scheduler de limpeza de histórico; critical local README
  repository/
    interfaces.go        → Contratos compartilhados e estreitos de persistência
    postgres/            → Implementações concretas com pgx; critical local README
  resilience/    → Rate limiter local/Redis para max_delivery_rate por subscription
  retry/         → Poller de eventos para retry + interface EventProcessor; critical local README
  clock/         → Abstração de relógio (testabilidade)
docs/
  audit.md       → Auditoria com evidências — leia antes de qualquer implementação
  next-steps.md  → Direções possíveis com estimativa de esforço
  learnings/     → Lições técnicas e decisões práticas extraídas da implementação
  spikes/        → Investigações arquiteturais propostas; não são decisões nem planos executáveis
  exec-plans/
    active/      → Único plano em execução
    queued/      → Planos futuros definidos, aguardando dependências
    done/        → Histórico de planos concluídos
  adr/           → ADRs de decisões arquiteturais
  product.md     → Fonte de verdade de produto: problema, usuários, promessas e limites
  v1-roadmap.md  → Sequência finita, critérios de conclusão e feature freeze do v1
  spec.md        → Contrato vivo de comportamento observável e invariantes
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
| Plano ativo | [docs/exec-plans/](docs/exec-plans/) | Consulte `active/`; se ausente, não há plano ativo |
| Planos enfileirados | [docs/exec-plans/queued/](docs/exec-plans/queued/) | Para entender a sequência futura; não implementar antes da promoção |
| Lições de implementação | [docs/learnings/](docs/learnings/) | Depois de mudanças relevantes ou para evitar repetir erros |
| Definição de produto | [docs/product.md](docs/product.md) | Para entender problema, usuários, promessas, limites e direção aceita para o v1 |
| Roadmap v1 | [docs/v1-roadmap.md](docs/v1-roadmap.md) | Para verificar se uma mudança fecha um critério do v1 ou está fora de escopo |
| Spec viva | [docs/spec.md](docs/spec.md) | Para entender comportamento observável e invariantes do sistema |
| Limitações e backlog | [docs/LIMITATIONS.md](docs/LIMITATIONS.md) | Para avaliar novas features |
| Decisões arquiteturais | [docs/adr/](docs/adr/) | Antes de propor mudanças estruturais |
| Spikes propostos | [docs/spikes/](docs/spikes/) | Para preservar hipóteses e perguntas ainda não aceitas |
| Contexto local de pacote | `internal/{api,app,kafka,retry,retention,repository/postgres}/README.md` | Antes de alterar um desses subsistemas críticos |
| Contexto de resilience | [internal/resilience/README.md](internal/resilience/README.md) | Antes de alterar destination protection ou rate limiting |

---

## External dependencies

Para rodar o projeto e os testes completos, as seguintes dependências precisam estar disponíveis:

| Dependência | Produção | Testes | Como subir |
|-------------|----------|--------|------------|
| PostgreSQL | Sim | Sim | `docker compose up postgres` / testcontainers (automático em testes) |
| Redis | Sim para limite distribuído | Sim | `docker compose up redis` / testcontainers (automático em testes) |
| Kafka | Sim | Sim (smoke E2E) | `docker compose -f docker-compose.kafka.yaml up` |

Testes que requerem Docker (sobem infra via testcontainers):
- `internal/repository/postgres/schema_test.go`
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

**Interfaces:** definidas no consumidor quando representam uma necessidade local (`EventPublisher`
e replay em `api/`, `EventHandler` em `kafka/`, `EventProcessor` em `retry/`). Contratos realmente
compartilhados ficam em `internal/repository/`; mantenha ambos estreitos e orientados por papel.

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

### Documentation authority

- One durable fact should have one authoritative home. Other docs may summarize it for navigation,
  but should link to the owner instead of repeating the full explanation.
- `README.md` is the entry point: short project description, quick start, main commands, and links.
  It does not own full product definition, behavior spec, architecture, or long runbooks.
- `docs/product.md` owns product purpose, users, promises, boundaries, maturity, and non-goals.
- `docs/spec.md` owns externally observable behavior, API semantics, delivery states, and system
  invariants.
- `docs/architecture.md` owns runtime structure, component boundaries, and implementation
  mechanisms.
- `docs/operations.md` owns run, validate, inspect, and failure-response guidance.
- `docs/LIMITATIONS.md` owns accepted limitations and possible future responses; it is not the
  active roadmap.
- `docs/v1-roadmap.md` owns the accepted v1 sequence and release gate.
- `PROGRESS.md` owns current verified state, validation evidence, and the next starting point.
- Completed exec plans are historical evidence, not current behavior authority.
- `internal/*/README.md` files own local package context only: ownership, invariants, hazards, and
  verification guidance.

Project description is layered: keep the shortest useful description in `README.md`, put complete
product meaning in `docs/product.md`, put caller/operator-visible behavior in `docs/spec.md`, and put
implementation structure in `docs/architecture.md`. If the same explanation appears in several of
those files, replace duplicates with links to the authoritative document.

Before adding durable documentation, ask: is this current behavior, planned behavior, or historical
context; which file owns that fact; can this file link instead of restating; and will this sentence
still be true after the current exec plan is done?

### Session start

1. Leia [PROGRESS.md](PROGRESS.md)
2. Execute `go build ./...` — confirme que o baseline compila
3. Leia o exec plan ativo em `docs/exec-plans/active/`
4. Continue do ponto onde o PROGRESS.md indica

### During work

1. Siga os steps do exec plan em ordem
2. Para cada step: escreva o teste primeiro, implemente, verifique
3. Commit após cada step completo usando Conventional Commits (`feat:`, `fix:`, `refactor:`,
   `docs:`, `test:`, `chore:`), mantendo commits reversíveis e focados
4. Não avance para o próximo step com testes falhando
5. Atualize `product.md` quando propósito, público, promessa ou limite mudar; atualize a spec quando o comportamento observável mudar; use ADR para registrar decisões técnicas
6. Ao entrar em um pacote crítico, leia o README local e atualize-o se invariantes ou ownership mudarem
7. Durante o feature freeze do v1, não promova trabalho que não feche um critério do roadmap ou um defeito que o ameace

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

# Tests (requer Docker para testes de postgres e E2E)
go test ./...

# Layered validation
go test -race ./internal/api/... ./internal/config/... ./internal/domain/... ./internal/kafka/... ./internal/observability/... ./internal/retention/... ./internal/retry/...
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
