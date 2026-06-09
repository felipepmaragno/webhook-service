# Progress — Dispatch

> Leia este arquivo PRIMEIRO em toda sessão.

---

## Verified state (2026-06-08, commit `e6227c38`)

| Check | Resultado |
|-------|-----------|
| Build | PASS |
| Tests | PASS — 0 failing |
| Race detector | PASS |
| Coverage total | **35.6%** |
| golangci-lint | Não executado (não instalado no ambiente de auditoria) |

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

---

## Known gaps

Comportamentos implementados sem cobertura de teste — ordenados por risco:

1. `repository/postgres/event.go` — **0%** — todas as queries de eventos sem teste
2. `repository/postgres/subscription.go` — **0%** — todas as queries de subscriptions sem teste
3. `kafka/consumer.go` — **0%** — consumer loop inteiro sem teste
4. `kafka/producer.go` — **0%** — producer sem teste
5. `api/handler.go` — GET /events/{id}/attempts, GET /subscriptions, DELETE /subscriptions sem teste
6. `api/handler.go` — CreateEvent, GetEvent, CreateSubscription com cobertura parcial (caminhos de erro descobertos)
7. Idempotency (event ID duplicado) — implementado via PK, sem teste explícito
8. Semáforo distribuído Redis — `redis_semaphore.go` implementado mas sem teste próprio

---

## Do NOT touch without a test first

- `internal/repository/postgres/event.go` (338 linhas, 0%)
- `internal/repository/postgres/subscription.go` (208 linhas, 0%)
- `internal/kafka/consumer.go` (252 linhas, 0%)
- `internal/kafka/producer.go` (251 linhas, 0%)
- `internal/api/handler.go` (253 linhas, 35.6%)

---

## Next session should

1. Ler este arquivo e o `docs/next-steps.md`
2. Aguardar decisão de direção do projeto
3. Após decisão: criar exec plan em `docs/exec-plans/active/`
4. Iniciar pelo Step 1 do exec plan criado
