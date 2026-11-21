# Architecture Decision Records

Decisions that shape this project, in [MADR](https://adr.github.io/madr/) format. New records start from [`adr-template.md`](adr-template.md).

| ADR | Title | Status | Date |
| --- | ----- | ------ | ---- |
| [0001](0001-ship-bb-as-a-single-go-binary-on-the-host.md) | Ship bb as a single Go binary on the host | accepted | 2026-11-14 |
| [0002](0002-put-all-capability-in-a-core-engine-with-thin-wrappers.md) | Put all capability in a core engine with thin wrappers | accepted | 2026-11-14 |
| [0003](0003-run-every-experiment-in-docker-compose.md) | Run every experiment in Docker Compose | accepted | 2026-11-14 |
| [0004](0004-use-experiment-yaml-and-files-instead-of-a-database.md) | Use experiment.yaml and files instead of a database | accepted | 2026-11-14 |
| [0005](0005-enable-tls-by-default-with-a-local-name-constrained-ca.md) | Enable TLS by default with a local name-constrained CA | accepted | 2026-11-14 |
| [0006](0006-use-official-client-libraries-instead-of-a-breadboard-sdk.md) | Use official client libraries instead of a Breadboard SDK | accepted | 2026-11-14 |
| [0007](0007-use-bkit-modules-for-breadboard-internals.md) | Use bkit modules for Breadboard internals | superseded by [0011](0011-fill-bkit-gaps-with-the-standard-library.md) | 2026-11-14 |
| [0008](0008-build-a-read-only-web-ui-with-templ-templui-and-tailwind.md) | Build a read-only web UI with templ, templUI and Tailwind | superseded by [0013](0013-build-web-ui-components-in-repo-with-templ-and-tailwind.md) | 2026-11-14 |
| [0009](0009-limit-exporters-to-kubernetes-ansible-and-terraform.md) | Limit exporters to Kubernetes, Ansible and Terraform for AWS, GCP and Cloudflare | accepted | 2026-11-14 |
| [0010](0010-use-seaweedfs-for-the-s3-compatible-catalog-entry.md) | Use SeaweedFS for the S3 Compatible catalog entry | accepted | 2026-11-14 |
| [0011](0011-fill-bkit-gaps-with-the-standard-library.md) | Fill bkit gaps with the standard library | accepted | 2026-11-15 |
| [0012](0012-read-service-data-for-the-web-ui-through-docker.md) | Read service data for the web UI through Docker | accepted | 2026-11-16 |
| [0013](0013-build-web-ui-components-in-repo-with-templ-and-tailwind.md) | Build web UI components in-repo with templ and Tailwind | accepted | 2026-11-16 |
| [0014](0014-serve-mcp-with-the-official-go-sdk.md) | Serve MCP with the official Go SDK | accepted | 2026-11-16 |
| [0015](0015-run-sidecars-in-a-pod-that-shares-the-apps-network-namespace.md) | Run sidecars in a pod that shares the app's network namespace | accepted | 2026-11-16 |
| [0016](0016-run-replicas-as-named-instances.md) | Run replicas as named instances | accepted | 2026-11-16 |
| [0017](0017-reach-lab-services-from-the-host-through-a-per-lab-host-router.md) | Reach lab services from the host through a per-lab `*.localhost` router with a host-only CA | accepted | 2026-11-17 |
| [0018](0018-use-grafana-otel-lgtm-for-the-otel-catalog-entry.md) | Use Grafana otel-lgtm for the `otel` catalog entry | accepted | 2026-11-20 |
| [0019](0019-use-official-opensearch-single-node-for-the-opensearch-catalog-entry.md) | Use official single-node OpenSearch for the `opensearch` catalog entry | accepted | 2026-11-21 |
| [0020](0020-use-official-nats-alpine-for-the-nats-catalog-entry.md) | Use official NATS Alpine image for the `nats` catalog entry | accepted | 2026-11-21 |
