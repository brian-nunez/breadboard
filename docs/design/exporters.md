# Exporters

Exporters turn an experiment into something that runs elsewhere. Each exporter consumes the **resolved model**, never a generated compose file.

## Rules

- **Explicit gaps.** When a target cannot represent a service, setting, or TLS policy, the output carries a warning file (`BREADBOARD-WARNINGS.md`) and the CLI prints it. A gap is never dropped silently.
- **Secrets through the target's medium.** Each exporter supplies its own secret materializer ([`security.md`](security.md)).
- **Pinned versions.** Generated output pins exact versions of images, Terraform providers, and Helm charts or Ansible collections.
- **Validated output.** Golden tests per exporter; plus each target's own validator when the tool is installed (`terraform validate`, `kubectl apply --dry-run=client`, `ansible-lint`).

## Compose (Phase 1)

`bb export compose --out <dir> [--secrets inline|dotenv]` writes:

- `docker-compose.yml`: infra and app services, health checks, limits, aliases, segments.
- `docker-compose.dev.yml`: bind mounts and dev-stage builds.
- `certs/`: CA certificate, leaf certificates and keys, trust bundle.
- `.env` and `.gitignore` when `--secrets dotenv` is used.
- `README.md`: how to run with plain `docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d`.

Bind paths are relative to the export directory, so the stack is portable. Plaintext edges carry their `reason` as a comment.

## Later targets

| Target | Phase | Approach |
|---|---|---|
| Kubernetes | 7 | Plain manifests: StatefulSets, Services, Secrets, NetworkPolicies for segments. CA exported as a cert-manager CA Issuer, or as static Secrets. |
| Ansible | 7 | Playbook that installs Docker, copies certs and secrets (Ansible Vault), and deploys the compose stack. |
| Terraform: AWS | 7 | Managed equivalents with provider-managed TLS; secrets in Secrets Manager. |
| Terraform: GCP | 7 | Managed equivalents with provider-managed TLS; secrets in Secret Manager. |
| Terraform: Cloudflare | 7 | Only what Cloudflare can host: R2 for S3 Compatible. Postgres, Redis, Kafka, and Consul are reported as gaps. |

Out of scope: Azure, Stripe.

### Managed service mapping (initial)

| Service | AWS | GCP | Cloudflare |
|---|---|---|---|
| Postgres | RDS for PostgreSQL | Cloud SQL for PostgreSQL | gap (Hyperdrive only fronts an external database) |
| Redis | ElastiCache | Memorystore | gap |
| Kafka | Amazon MSK | Managed Service for Apache Kafka | gap (Queues is not Kafka) |
| Consul | self-managed on EC2/EKS | self-managed on GCE/GKE | gap |
| S3 Compatible | Amazon S3 | Cloud Storage (XML API / HMAC keys) | R2 |

On cloud targets, the Breadboard CA is a local-only construct: exports default to the provider's TLS and treat a managed private CA as opt-in, because those services carry fixed monthly costs.
