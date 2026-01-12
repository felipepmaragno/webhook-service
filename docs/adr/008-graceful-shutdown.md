# ADR 008: Graceful Shutdown

## Status
Accepted

## Context
When the service stops (deploy, scale-down, restart):
- In-flight deliveries should complete
- No events should be lost
- No duplicate deliveries on restart

## Decision
Use **context cancellation** with worker drain.

Shutdown sequence:
1. Receive SIGTERM/SIGINT
2. Cancel worker context
3. Wait for workers to finish current deliveries
4. Close database connections
5. Exit

## Alternatives Considered

### Immediate Shutdown
```go
os.Exit(0)
```
**Cons:**
- In-flight requests aborted
- Possible data corruption
- Poor user experience

### Timeout-Based Shutdown
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
```
**Cons:**
- May kill long-running deliveries
- Arbitrary timeout selection

### Kubernetes preStop Hook Only
**Cons:**
- Doesn't handle non-Kubernetes deployments
- Less control over shutdown sequence

## Rationale

### 1. Signal Handling

```go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

go func() {
    <-sigChan
    logger.Info("shutdown signal received")
    cancel() // Cancel context
}()
```

### 2. Context Propagation

All workers receive context:
```go
func (p *Pool) Start(ctx context.Context) {
    ctx, p.cancel = context.WithCancel(ctx)
    for i := 0; i < p.config.Workers; i++ {
        p.wg.Add(1)
        go p.worker(ctx, i)
    }
}
```

Workers check context in their loop:
```go
func (p *Pool) worker(ctx context.Context, id int) {
    defer p.wg.Done()
    ticker := time.NewTicker(p.config.PollInterval)
    
    for {
        select {
        case <-ctx.Done():
            p.logger.Info("worker stopping", "id", id)
            return
        case <-ticker.C:
            p.processBatch(ctx)
        }
    }
}
```

### 3. Wait for Workers

```go
func (p *Pool) Stop() {
    if p.cancel != nil {
        p.cancel()
    }
    p.wg.Wait() // Block until all workers finish
    p.logger.Info("all workers stopped")
}
```

### 4. Event Safety

Events are safe because of database transactions:
- Event claimed with `FOR UPDATE`
- If worker dies mid-delivery, transaction rolls back
- Event returns to pending state
- Another worker picks it up

```go
func (p *Pool) deliverEvent(ctx context.Context, event *domain.Event) {
    // If context cancelled here, delivery attempt may complete
    // but that's okay - we update the event status in DB
    
    // If we crash before DB update, transaction rolls back
    // Event stays in 'processing' but lock is released
}
```

### 5. HTTP Server Shutdown

```go
server := &http.Server{Addr: addr, Handler: router}

go func() {
    <-ctx.Done()
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    server.Shutdown(shutdownCtx)
}()
```

`server.Shutdown`:
- Stops accepting new connections
- Waits for active requests to complete
- Times out after 10 seconds

## Kubernetes Integration

```yaml
spec:
  terminationGracePeriodSeconds: 60
  containers:
  - name: dispatch
    lifecycle:
      preStop:
        exec:
          command: ["/bin/sh", "-c", "sleep 5"]
```

Sequence:
1. Pod marked for termination
2. preStop hook runs (5s sleep for load balancer drain)
3. SIGTERM sent to container
4. Application graceful shutdown
5. After 60s, SIGKILL if still running

## Consequences

### Positive
- No lost events
- Clean shutdown
- Works in any environment
- Kubernetes-friendly

### Negative
- Shutdown takes time (workers must finish)
- Long deliveries delay shutdown

### Mitigations
- HTTP timeout limits delivery duration (30s default)
- Kubernetes terminationGracePeriodSeconds as backstop
- Monitoring for slow shutdowns

### Future Considerations
- Configurable shutdown timeout
- Metrics for shutdown duration
- Drain endpoint for load balancers
