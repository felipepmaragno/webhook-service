# Next Steps — Dispatch

> Produzido em: 2026-06-08, baseline commit `e6227c38`
> Baseado em: [docs/audit.md](audit.md)
> Decisão pendente: escolha uma ou mais direções abaixo para virar exec plan.

---

## Contexto

O dispatch funciona e passa em todos os testes. A cobertura total é 35.6%, concentrada
nos componentes mais críticos sem teste: as queries do PostgreSQL (0%), o consumer e producer
Kafka (0%), e metade dos handlers HTTP. A lógica de delivery em si é bem testada (78–95%).
O risco real está na camada de I/O — qualquer mudança nela é cega.

---

## Direção 1: Estabilizar o baseline (dívida técnica)

**O que é:** Elevar cobertura das camadas de I/O antes de qualquer evolução.
Sem isso, qualquer mudança no sistema toca código não verificado.

**Gaps a cobrir, por prioridade:**

| Pacote | Cobertura atual | Meta mínima | Risco se não cobrir |
|--------|-----------------|-------------|---------------------|
| `repository/postgres/event.go` | 0% | 70% | Regressão silenciosa em queries de eventos |
| `repository/postgres/subscription.go` | 0% | 70% | Idem para subscriptions |
| `api/handler.go` | 35.6% | 75% | Endpoints sem teste de erro retornam comportamento errado sem avisar |
| `kafka/consumer.go` | 0% | 60% | Consumer loop — perda de evento não detectada |
| `kafka/producer.go` | 0% | 60% | Publish falha silenciosamente |

**Abordagem para repository/postgres:** usar testcontainers (padrão já estabelecido
no `batcher_test.go`) — sobe PostgreSQL real, aplica migrations, testa as queries diretamente.

**Abordagem para kafka:** mocks para producer/consumer (padrão já estabelecido no
`handler_test.go`) — não requer Kafka real, testa o comportamento sob falhas.

**Abordagem para api:** padrão já existente em `handler_test.go` com `httptest.NewRecorder`
e mocks locais — expandir cobertura dos caminhos de erro e endpoints faltantes.

**Esforço estimado:** 3–4 sessões de trabalho focado.
**Base:** 5 arquivos críticos, ~1000 linhas sem cobertura, infra de teste já existe
(testcontainers configurado, mocks estabelecidos — não há setup novo a fazer).

**Quando escolher:** se qualquer outra direção for escolhida, esta é pré-requisito
para as áreas impactadas. Pode ser feita em paralelo com Direção 2 se as áreas não
se sobrepuserem.

---

## Direção 2: Dead Letter Queue + replay de eventos

**O que é:** Implementar DLQ para eventos que esgotaram tentativas.
Hoje ficam como `status = 'failed'` na tabela principal — sem mecanismo de replay.

**Impacto no código existente:**
- Nova migration: tabela `dead_letter_events` ou coluna `dead_lettered_at`
- Alteração em `kafka/delivery.go:deliverToSubscription` — após `MarkAsFailed`, mover para DLQ
- Novo endpoint: `POST /events/{id}/replay` — requeue do evento
- Arquivos de alto risco afetados: `repository/postgres/event.go` (0% cobertura)

**Dependência:** requer cobertura de `event.go` (Direção 1) antes de tocar nele.
Sem testes, a mudança em `delivery.go` pode quebrar o fluxo de retry existente sem avisar.

**Esforço estimado:** 1–2 sessões após Direção 1 para `event.go`.
**Base:** 1 nova migration, ~50 linhas em `delivery.go`, ~80 linhas de repositório novo,
1 endpoint novo — escopo bem delimitado.

**Nota do LIMITATIONS.md:** estimativa original era 2–3 dias — realista se o baseline
estiver coberto.

---

## Direção 3: Testes de integração end-to-end

**O que é:** Testes que sobem API + Worker + infra completa (PostgreSQL + Redis + Kafka)
e verificam o fluxo completo: POST /events → Kafka → worker → delivery → status `delivered`.

**Impacto no código existente:**
- Nenhuma alteração em código de produção
- Novo arquivo `integration_test.go` ou pacote `tests/integration/`
- Requer Kafka disponível (hoje só PostgreSQL e Redis têm testcontainers configurados)
- Módulo `testcontainers-go` tem suporte a Kafka — não requer dependência nova

**Por que vale considerar:** os testes atuais cobrem componentes isolados. Nenhum teste
verifica que o sistema inteiro funciona de ponta a ponta. Se o contrato entre API e worker
quebrar (mudança no schema Kafka, por exemplo), os testes atuais não detectam.

**Esforço estimado:** 1–2 sessões.
**Base:** setup de testcontainers para Kafka (~2h de trabalho), ~100–150 linhas de teste,
infra de testcontainers já conhecida no projeto.

---

## Direção 4: Multi-tenancy

**O que é:** Adicionar `tenant_id` a eventos e subscriptions para isolar contextos.

**Impacto no código existente:**
- Migration: adicionar `tenant_id` a `events` e `subscriptions`
- Todas as queries em `event.go` e `subscription.go` precisam de filtro por tenant
- API precisa de autenticação/identificação de tenant
- Todos os handlers em `api/handler.go` são afetados
- Arquivos de alto risco afetados: todos os de 0% cobertura + api/handler.go

**Dependência:** requer cobertura completa de Direção 1 antes de começar.
Tocar em `event.go`, `subscription.go` e `handler.go` sem testes é risco inaceitável.

**Esforço estimado:** 3–5 sessões (após Direção 1).
**Base:** estimativa do LIMITATIONS.md era 1–2 semanas — plausível considerando
autenticação, migrations, mudanças em todas as queries e handlers.

**Quando escolher:** quando o dispatch precisar servir múltiplos clientes/contextos.
Para uso interno single-tenant, não é prioridade.

---

## O que não está listado

- **Payload transformation** (LIMITATIONS.md #4) — descartado por agora. Alta complexidade
  (scripting, templates), baixo impacto imediato. Avaliar quando clientes exigirem.
- **Webhook verification/handshake** (LIMITATIONS.md #7) — descartado por agora.
  Útil mas não crítico. A spec original não o requeria para MVP.
- **Batch delivery** (LIMITATIONS.md #9) — descartado. Requer mudança de contrato
  com os destinatários. Complexidade desproporcional ao ganho atual.
- **Refatoração de interfaces** (inconsistência EventRepository no pacote errado) —
  descartado. A inconsistência existe, mas não causa bugs. Corrigir sem benefício
  funcional não justifica o risco.

---

## Como usar este documento

1. Leia as direções e suas dependências
2. Escolha uma ou mais (podem ser sequenciadas)
3. Comunique a decisão
4. O agente cria o exec plan correspondente em `docs/exec-plans/active/`
   seguindo o formato do `harness-engineering-guide.md`

**Recomendação baseada na auditoria:** Direção 1 é pré-requisito real para qualquer
outra. O risco de trabalhar em cima de 0% de cobertura nas camadas críticas não é teórico
— é inevitável. A questão é quando ele se materializa.
