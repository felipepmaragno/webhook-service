# Audit — Dispatch

> Auditado em: 2026-06-08
> Baseline commit: `e6227c38d4d3eba91988466860211ecc0932a6b8`
> golangci-lint: não instalado no ambiente de auditoria — não executado

---

## Snapshot mecânico

> Atualizado em 2026-06-09 (exec plan v0.2.0)

| Check | Resultado |
|-------|-----------|
| `go build ./...` | PASS |
| `go test ./...` | PASS — 0 failures |
| `go test -race ./...` | PASS |
| `golangci-lint` | Não instalado — não executado |
| Cobertura total | **51.9%** (era 35.6% em 2026-06-08) |

---

## Dependências externas

Serviços requeridos para rodar o projeto e os testes completos:

| Dependência | Produção | Testes | Como subir |
|-------------|----------|--------|------------|
| PostgreSQL | Sim | Sim | `docker compose up postgres` / testcontainers (automático) |
| Redis | Sim (opcional — fallback in-memory) | Sim | `docker compose up redis` / testcontainers (automático) |
| Kafka | Sim | Sim (mocks) | `docker compose up` (docker-compose.kafka.yaml) |

Testes que sobem infra via testcontainers (requerem Docker):
- `internal/repository/postgres/batcher_test.go` — sobe PostgreSQL via testcontainers
- `internal/resilience/redis_circuitbreaker_test.go` — sobe Redis via testcontainers (inferido pelo módulo)
- `internal/resilience/redis_ratelimiter_test.go` — idem

Nota: testes de `kafka` usam mocks — não requerem Kafka real para rodar.

---

## Cobertura por pacote

> Atualizado em 2026-06-09 após exec plan v0.2.0

| Pacote | Cobertura | Delta | Risco |
|--------|-----------|-------|-------|
| `internal/config` | 98.0% | — | Baixo |
| `internal/retry` | 95.7% | — | Baixo |
| `internal/domain` | 90.0% | — | Baixo |
| `internal/repository/postgres` | **89.8%** | +67pp | Baixo |
| `internal/resilience` | 56.8% | — | Médio |
| `internal/api` | **55.4%** | +20pp | Médio |
| `internal/kafka` | **64.2%** | +19pp | Médio |
| `internal/observability` | 39.1% | — | Médio |
| `internal/clock` | 0.0% | — | Baixo (utilitário simples) |
| `internal/repository` (interfaces) | N/A | — | — |
| `cmd/*` | 0.0% | — | Baixo (wiring/bootstrap) |
| `scripts/*` | 0.0% | — | Fora de escopo |

---

## Análise detalhada dos gaps críticos

### `internal/api` — 35.6% — Alto risco

Funções com 0% de cobertura:
- `GetEventAttempts` — endpoint GET /events/{id}/attempts — sem nenhum teste
- `GetSubscriptions` — endpoint GET /subscriptions — sem nenhum teste
- `DeleteSubscription` — endpoint DELETE /subscriptions/{id} — 33.3% (só happy path)
- `WithMetrics` — método de configuração — 0%
- `NewRouter` — sem teste

Funções parcialmente testadas:
- `CreateEvent` — 60% (falta: body inválido, publisher falha, ID duplicado)
- `GetEvent` — 61.5% (falta: evento não encontrado)
- `CreateSubscription` — 56.2% (falta: URL inválida, payload malformado)

### `internal/repository/postgres` — 22.7% — Alto risco

`event.go` (338 linhas) — **0% de cobertura em todas as funções**:
- `Create`, `CreateBatch`, `GetByID`, `GetPendingEvents`, `UpdateStatus`
- `RecordAttempt`, `UpdateStatusBatch`, `RecordAttemptBatch`, `GetAttemptsByEventID`

`subscription.go` (208 linhas) — **0% de cobertura em todas as funções**:
- `Create`, `GetByID`, `GetActive`, `GetByEventType`, `GetByEventTypes`, `Delete`

`batcher.go` — bem coberto (93%+): `Add`, `run`, `flushLocked`, `executeBatch`, `batchInsert`

**Nota crítica:** o único teste existente para `repository/postgres` é `batcher_test.go`,
que testa o batcher em isolamento com testcontainers. As queries reais de eventos
e subscriptions não têm nenhum teste.

### `internal/kafka` — 45.5% — Alto risco

Funções com 0%:
- `consumer.go` inteiro: `NewConsumer`, `Start`, `Stop`, `consumeLoop`, `collectBatch`, `processBatchAndCommit`, `commitMessages`, `Stats`
- `producer.go` inteiro: `NewProducer`, `Publish`, `PublishBatch`, `Close`
- `NewDeliveryHandler`, `ProcessEvents` (ponto de entrada do handler)

Funções bem cobertas:
- `ProcessBatch` — 95.5% ✓
- `deliverEvent` — 94.1% ✓
- `deliverWebhook` — 88.5% ✓
- `deliverToSubscription` — 78.1% ✓

**Diagnóstico:** a lógica de delivery é bem testada via `handler_test.go`.
O que não tem teste é a camada de I/O real: producer/consumer Kafka e
o consumer loop inteiro. Os testes existentes usam mocks.

---

## O que funciona (evidência mecânica)

- [x] **Event domain entity e state machine** — `TestEventStateTransitions` em `domain/event_test.go` — 90% cobertura
- [x] **Subscription domain entity e wildcard matching** — `TestSubscriptionMatchesEventType` em `domain/subscription_test.go` — 90% cobertura
- [x] **Config parsing (API e Worker)** — `config/config_test.go` — 98% cobertura
- [x] **ProcessBatch (delivery handler)** — `kafka/handler_test.go` — 95.5%
- [x] **deliverEvent com retry** — `kafka/handler_test.go` — 94.1%
- [x] **deliverWebhook (HTTP delivery + HMAC)** — `kafka/webhook.go` — testado indiretamente via handler_test
- [x] **deliverToSubscription** — `kafka/handler_test.go` — 78.1%
- [x] **EventBatcher (batch inserts)** — `repository/postgres/batcher_test.go` — 93%+ com PostgreSQL real via testcontainers
- [x] **Circuit breaker in-memory** — `resilience/circuitbreaker_test.go` — testado
- [x] **Rate limiter in-memory** — `resilience/ratelimiter_test.go` — testado
- [x] **Circuit breaker Redis** — `resilience/redis_circuitbreaker_test.go` — testado com Redis via testcontainers
- [x] **Rate limiter Redis** — `resilience/redis_ratelimiter_test.go` — testado
- [x] **Retry poller** — `retry/poller_test.go` — 95.7%
- [x] **Health handler** — `observability/health_test.go` — testado
- [x] **Metrics** — `observability/metrics_test.go` — testado
- [x] **Logging middleware** — `observability/middleware.go` — incluído em observability (39.1%)
- [x] **POST /events handler** — `api/handler_test.go` — 60% (happy path testado)
- [x] **GET /events/{id} handler** — `api/handler_test.go` — 61.5%
- [x] **POST /subscriptions handler** — `api/handler_test.go` — 56.2%

---

## Gaps vs spec

| Feature descrita no spec | Status real |
|--------------------------|-------------|
| POST /events | Implementada, parcialmente testada (60%) |
| GET /events/{id} | Implementada, parcialmente testada (61.5%) |
| GET /events/{id}/attempts | Implementada, **sem teste** |
| POST /subscriptions | Implementada, parcialmente testada (56.2%) |
| GET /subscriptions | Implementada, **sem teste** |
| DELETE /subscriptions/{id} | Implementada, parcialmente testada (33.3%) |
| GET /health | Implementada, testada |
| GET /metrics | Implementada |
| Retry com exponential backoff | Implementado, testado (95.7%) |
| Rate limiting por subscription (in-memory) | Implementado, testado |
| Rate limiting por subscription (Redis) | Implementado, testado |
| Circuit breaker (in-memory) | Implementado, testado |
| Circuit breaker (Redis distribuído) | Implementado, testado |
| Idempotency por event ID | Implementado (PostgreSQL PRIMARY KEY) — sem teste explícito |
| HMAC-SHA256 signature | Implementado, testado indiretamente |
| Graceful shutdown | Implementado nos dois binários — sem teste automatizado |
| Semáforo distribuído (Redis) | Implementado — `redis_semaphore.go` sem teste próprio |
| Kafka como fila de eventos | Implementado — consumer/producer sem testes de integração |
| `EventStatus.throttled` | Implementado no domain — **não documentado no spec** (divergência) |

---

## Decisões de design implícitas

Decisões que o código toma e que não estão em ADR, ou que divergem do spec:

- **`throttled` como status extra** — O spec define 5 estados (pending, processing, delivered, retrying, failed). O código tem 6 — adiciona `throttled` para rate limit / circuit breaker open. Está correto como design, mas não está no spec.
- **Interfaces definidas no consumidor** — `EventPublisher` está em `internal/api/`, `EventHandler` em `internal/kafka/`, `EventProcessor` em `internal/retry/`. Padrão Go idiomático — mas inconsistente: `EventRepository` e `SubscriptionRepository` estão em `internal/repository/` (pacote separado), não no consumidor.
- **Redis opcional com fallback in-memory** — o worker sobe sem Redis e usa implementações in-memory. O spec não documenta esse comportamento de degradação.
- **Dois binários separados** (`cmd/dispatch` e `cmd/worker`) — o spec mostra um único sistema, mas a implementação evoluiu para microserviços. Documentado em ADR-014.
- **`semaphore` é `nil` quando Redis está indisponível** — `initResilience` retorna semaphore como `nil` no fallback in-memory. O `buildDeliveryHandler` trata isso com `if semaphore != nil`. Comportamento correto mas não documentado.

---

## Convenções extraídas do código

Baseadas em leitura direta e greps — apenas o que foi verificado:

- **Logging:** `log/slog` com `slog.NewJSONHandler` — **100% consistente** em todos os entry points e pacotes
- **Error handling:** `fmt.Errorf("context: %w", err)` — **83% das ocorrências** (25 de 30). `errors.New` usado em 5 casos para erros sem wrap.
- **Context:** primeiro parâmetro — **64 de ~195 funções** têm `ctx context.Context` como primeiro parâmetro. Consistente em I/O, ausente em utilitários/helpers (correto).
- **Interfaces:** maioria no consumidor (`api`, `kafka`, `retry`) — exceto repositórios que ficam em `internal/repository/` (inconsistência menor).
- **Functional options pattern:** usado em `kafka.DeliveryHandler` (`WithXxx` options) — padrão consolidado neste pacote.
- **Structs retornados, interfaces aceitas:** seguido consistentemente.
- **Testes com mocks locais:** mocks definidos no próprio `_test.go`, não em pacote separado — padrão consistente.

---

## Áreas de alto risco

Nenhuma alteração nestes arquivos sem escrever testes primeiro:

- `internal/repository/postgres/event.go` (338 linhas, **0% cobertura**) — toda a camada de persistência de eventos está sem teste. Qualquer mudança aqui é risco máximo.
- `internal/repository/postgres/subscription.go` (208 linhas, **0% cobertura**) — idem para subscriptions.
- `internal/kafka/consumer.go` (252 linhas, **0% cobertura**) — consumer loop inteiro sem teste. Bugs aqui causam perda silenciosa de eventos.
- `internal/kafka/producer.go` (251 linhas, **0% cobertura**) — producer sem teste. Falha aqui = eventos não entram na fila.
- `internal/api/handler.go` (253 linhas, **35.6%**) — 3 handlers completamente sem teste, 3 com cobertura parcial.
