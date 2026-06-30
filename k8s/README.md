# Kubernetes Deployment Boundary

The base manifests do not create `dispatch-secrets`. Provision that Secret before applying the
Kustomize base, preferably through the cluster's external secret controller or secret manager.

For a disposable local cluster only:

```bash
kubectl -n dispatch create secret generic dispatch-secrets \
  --from-literal=DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/dispatch?sslmode=require' \
  --from-literal=REDIS_URL='redis://HOST:6379/0' \
  --from-literal=KAFKA_BROKERS='BROKER:9093'
kubectl apply -k k8s/
```

Do not commit the generated Secret or real connection values. The API Service is `ClusterIP`; keep
it private or place it behind an authenticating gateway. Restrict worker metrics and datastore
traffic with cluster/network policy appropriate to the environment.

The manifests use `/health` for liveness and `/ready` for readiness. Readiness is application-owned:
API pods check PostgreSQL and Kafka topic metadata; worker pods also check Redis when `REDIS_URL` is
configured. Liveness intentionally stays shallow so Kubernetes does not restart healthy processes
during temporary dependency outages.

See [deployment security](../docs/deployment-security.md) for the supported trust model and
remaining operator responsibilities.
