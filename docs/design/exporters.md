# Exporters

Exporters turn an experiment into something that runs elsewhere. Each exporter consumes the **resolved model**, never a generated compose file.

## Rules

- **Explicit gaps.** When a target cannot represent a service, setting, or TLS policy, the output carries a warning file (`BREADBOARD-WARNINGS.md`) and the CLI prints it. A gap is never dropped silently.
- **Secrets through the target's medium.** Each exporter supplies its own secret materializer ([`security.md`](security.md)).
- **Pinned versions.** Generated output pins exact versions of images, Terraform providers, and Helm charts or Ansible collections.
- **Validated output.** Golden tests per exporter; plus each target's own validator when the tool is installed (`terraform validate`, `kubectl apply --dry-run=client`, `ansible-lint`).

## Compose (Phase 1)

`bb export compose [--out <dir>] [--secrets inline|dotenv]` writes (default directory `export/compose` in the project):

- `docker-compose.yml`: infra and app services (built from their `dev` stage), health checks, limits, aliases, segments.
- `docker-compose.dev.yml`: source bind mounts and Go cache volumes for app services.
- `ca.pem`, `trust-bundle.pem`, `tls/<service>/`: the CA, the trust bundle, and each service's certificate and key.
- `config/<service>/`: generated server configuration (Kafka properties, `pg_hba.conf`, Consul config).
- `.env` (mode `0600`) when `--secrets dotenv` is used.
- `.gitignore`: private keys always, plus `.env` in dotenv mode or `docker-compose.yml` in inline mode (it then holds secrets).
- `README.md`: how to run with plain `docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --wait`.
- `.bb-export`: a marker. bb replaces a directory holding it on the next export and refuses to write into any other non-empty directory.

Bind paths are relative to the export directory, so the stack is portable. Plaintext edges carry their `reason` as a comment on the segment network and the client's environment. The export uses the same compose project name (`bb-<experiment>`) as `bb up`, so stop one before starting the other. Leaf certificates expire after 90 days; export again to renew them.

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
