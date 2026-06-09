# Progress — Dispatch

> Leia este arquivo PRIMEIRO em toda sessão.

---

## Verified state (2026-06-09, exec plan v0.2.0 concluído)

| Check | Resultado |
|-------|-----------|
| Build | PASS |
| Tests | PASS — 0 failing |
| Race detector | PASS (api + kafka) |
| Coverage total | **51.9%** (era 35.6%) |
| golangci-lint | Não executado (não instalado no ambiente) |

### Cobertura por pacote (atualizado)

| Pacote | Cobertura | Delta |
|--------|-----------|-------|
| `internal/retry` | 95.7% | — |
| `internal/config` | 98.0% | — |
| `internal/domain` | 90.0% | — |
| `internal/repository/postgres` | **89.8%** | +67pp |
| `internal/kafka` | **64.2%** | +19pp |
| `internal/api` | **55.4%** | +20pp |
| `internal/resilience` | 56.8% | — |
| `internal/observability` | 39.1% | — |
| `internal/clock` | 0.0% | — (utilitário simples) |
| `cmd/*` | 0.0% | — (wiring/bootstrap) |

---

## What works (mechanically verified)

- Event domain entity e state machine (pending → processing → delivered/retrying/throttled/failed)
- Subscription domain entity com wildcard matching (`order.*`)
- Config parsing para API e Worker
- Delivery pipeline: ProcessBatch → deliverEvent → deliverWebhook (bem testado)
- HMAC-SHA256 signature na entrega
- Retry com exponential backoff
- Rate limiting por subscription (in-memory e Redis)
- Circuit breaker por subscription (in-memory e Redis)
- EventBatcher para batch inserts no PostgreSQL
- Retry poller (polling de eventos para retry)
- Health e readiness handlers
- Métricas Prometheus
- Graceful shutdown nos dois binários (não testado automaticamente)
- EventRepository — todas as operações testadas contra PostgreSQL real (testcontainers)
- SubscriptionRepository — todas as operações testadas contra PostgreSQL real
- Consumer — collect/process/commit testados com fakeReader injetável
- Producer — Publish/PublishBatch testados com fakeWriter injetável
- API handlers — todos os endpoints cobertos incluindo caminhos de erro

---

## Refactorings aplicados (v0.2.0)

- `repository/interfaces.go`: `Shutdown` adicionado à interface `EventRepository` (inconsistência corrigida)
- `kafka/consumer.go`: extraída interface `MessageReader` — consumer testável sem Kafka real
- `kafka/producer.go`: extraída interface `MessageWriter` — producer testável sem Kafka real
- `kafka/consumer.go`: `NewConsumerWithReader` adicionado para injeção em testes
- `kafka/producer.go`: `NewProducerWithWriter` adicionado para injeção em testes

---

## Known gaps (residuais)

1. `api/handler.go` — 55.4%: cobertura de rotas ainda pode ser expandida (NewRouter test)
2. `internal/observability` — 39.1%: middleware de logging/tracing não coberto
3. `kafka/producer.go` — `PublishBatch` não propaga trace ID (bug documentado — ver audit.md)
4. `internal/resilience` — semáforo Redis não tem teste próprio
5. `cmd/*` — bootstrap/wiring sem testes (aceitável)

---

## Do NOT touch without a test first

- `internal/kafka/consumer.go` — testar com `NewConsumerWithReader` + `fakeReader`
- `internal/kafka/producer.go` — testar com `NewProducerWithWriter` + `fakeWriter`
- `internal/repository/postgres/event.go` — testar com `setupIntegrationDB`
- `internal/repository/postgres/subscription.go` — idem
- `internal/api/handler.go` — testar com mocks já definidos em `handler_test.go`

---

## Next session should

1. Ler este arquivo e o `docs/next-steps.md`
2. Escolher próxima direção: Direção 2 (refatoração `kafka/`) ou Direção 3 (observabilidade)
3. Criar exec plan em `docs/exec-plans/active/`
4. Iniciar pelo Step 1 do exec plan criado
