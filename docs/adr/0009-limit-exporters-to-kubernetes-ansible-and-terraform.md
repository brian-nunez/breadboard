---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Limit exporters to Kubernetes, Ansible and Terraform for AWS, GCP and Cloudflare

## Context and Problem Statement

When an experiment is worth keeping, Breadboard exports it to platforms beyond Docker Compose. Every target adds a mapping for each catalog entry, a secret materializer and validation tooling, so the set of targets is a long-term maintenance commitment. Which targets should Breadboard support?

## Decision Drivers

* Each target must be able to represent most of the catalog, or report gaps explicitly.
* Maintenance cost grows with targets times catalog entries.
* Breadboard runs real services, not mocks, including in exports.

## Considered Options

* Kubernetes, Ansible, and Terraform for AWS, GCP and Cloudflare
* The above plus Terraform for Azure and Stripe
* Compose export only

## Decision Outcome

Chosen option: "Kubernetes, Ansible, and Terraform for AWS, GCP and Cloudflare", because it covers self-hosted, VM-based and the two major managed-cloud paths that Brian uses, while keeping the matrix maintainable. Azure and Stripe were dropped by Brian.

Exporters consume the resolved model, never a generated compose file. A service a target cannot represent is listed in `BREADBOARD-WARNINGS.md` and printed by the CLI; nothing is dropped silently. Cloudflare can host only S3 Compatible (as R2); Postgres, Redis, Kafka and Consul are reported as gaps there.

### Consequences

* Good, because the target matrix stays small enough to test with golden files and each target's validator.
* Good, because explicit gap reporting keeps exports honest.
* Bad, because Azure users get no Terraform export.
* Bad, because the Cloudflare exporter is mostly gaps until Cloudflare hosts more of the catalog.

### Confirmation

Each exporter has golden tests and runs the target's own validator when installed (`terraform validate`, `kubectl apply --dry-run=client`, `ansible-lint`). Review checks that every catalog entry has a mapping or an explicit gap for every target.

## Pros and Cons of the Options

### Kubernetes, Ansible, and Terraform for AWS, GCP and Cloudflare

* Good, because it covers the common deployment paths.
* Bad, because Azure is not covered.

### The above plus Terraform for Azure and Stripe

* Good, because it covers more platforms.
* Bad, because Stripe is not infrastructure for these services and would need a different model.
* Bad, because each extra provider multiplies mapping and test work.

### Compose export only

* Good, because it is the cheapest to maintain.
* Bad, because users get no path from an experiment to a real deployment target.

## More Information

Recorded as D-017 in `docs/design/decisions.md`. Target details and managed-service mappings are in `docs/design/exporters.md`.
