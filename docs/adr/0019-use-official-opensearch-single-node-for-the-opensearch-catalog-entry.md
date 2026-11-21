---
status: "accepted"
date: 2026-11-21
decision-makers: Brian Nunez
---

# Use official single-node OpenSearch for the `opensearch` catalog entry

## Context and Problem Statement

Breadboard teaches distributed data stores, message queues, caching, service discovery, proxies, and observability, but has no full-text search and analytics engine. Learners need to practice inverted indexes, analyzers vs `keyword` fields, BM25 relevance scoring vs filter caching, faceted aggregations, CQRS/CDC read-model projections (`postgres` → `kafka` → `opensearch`), and zero-downtime reindexing with index aliases. Which image, security model, and topology should back the `opensearch` catalog entry?

## Decision Drivers

* Every catalog entry is one container with an exact pinned official image, TLS by default verified against the lab CA, a real TLS health check, and bounded `mem_limit` and `cpus`.
* OpenSearch's Security plugin must remain enabled with real TLS on both the REST (`9200`) and transport (`9300`) layers — never `DISABLE_SECURITY_PLUGIN=true` and never OpenSearch's unsafe demo certificates.
* Generated credentials must fit Breadboard's `.bb/secrets.json` secret policy without breaking URL/shell safety or requiring manual passwords.
* Starters and `bb add app` must use the official Go client (`github.com/opensearch-project/opensearch-go/v5`) configured strictly from environment variables, verifying TLS through `SSL_CERT_FILE`.
* OpenSearch's REST API should be reachable from the host when `host: true` is set through `bb-host` (ADR-0017), and inspectable in Breadboard's read-only Web UI and MCP via Docker exec (ADR-0012).

## Considered Options

* Official `opensearchproject/opensearch:3.10.0` single-node container with Security plugin enabled, lab CA TLS, and Breadboard's built-in Explorer
* `opensearchproject/opensearch` bundled or paired with a separate `opensearch-dashboards` container
* Meilisearch or Typesense instead of OpenSearch

## Decision Outcome

Chosen option: "Official `opensearchproject/opensearch:3.10.0` single-node container with Security plugin enabled, lab CA TLS, and Breadboard's built-in Explorer".

Breadboard mounts generated configuration into the container:

* `opensearch.yml` configures single-node mode (`discovery.type: single-node`), binds HTTP/REST to `0.0.0.0:9200` over HTTPS using the service's lab certificate (`/etc/bb/tls/tls.crt`, `/etc/bb/tls/tls.key`, `/etc/bb/ca.pem`), binds inter-node transport (`9300`) strictly to `127.0.0.1` over TLS with `plugins.security.nodes_dn` set to the service certificate's exact subject DN (`CN=<fqdn>,O=Breadboard`), and enables `plugins.security.allow_default_init_securityindex: true`.
* `DISABLE_INSTALL_DEMO_CONFIG=true` prevents OpenSearch's demo script from writing unsafe demo certificates or default demo passwords; `DISABLE_PERFORMANCE_ANALYZER_AGENT_CLI=true` disables the unencrypted `9600` Performance Analyzer agent and saves memory.
* `start.sh` stages the lab certificates into `/usr/share/opensearch/config/bb-certs/` with `0600` permissions, hashes the generated `.bb/secrets.json` `password` secret via OpenSearch's bundled `hash.sh` into `internal_users.yml` for the `admin` user (removing all default demo accounts), optionally pre-creates indices named in `config.indices`, and execs OpenSearch's entrypoint.
* Listeners declare `https` on `9200` only (no plaintext listener; `tls.mode: off` is refused, and `host: true` routes `https://<name>.<experiment>.localhost:<port>` to `:9200`).
* Resource defaults are `1536m` memory (`OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m`, leaving headroom for Lucene off-heap page cache and OS overhead) and `1.0` CPU, with a named data volume at `/usr/share/opensearch/data`.

### Consequences

* Good, because `bb add opensearch` delivers a real, TLS-verified, authenticated OpenSearch 3.x node with zero demo certs, zero plaintext listeners, and predictable single-container resource usage.
* Good, because Breadboard's read-only Explorer (`OpenSearchIndices`) and `host: true` HTTPS access let learners inspect cluster health, indices, shards, doc counts, and aliases without paying the memory cost of an extra Node.js Dashboards container.
* Good, because starters can pair `opensearch` directly with `postgres` and `kafka` using the official `opensearch-go/v5` client and standard `OPENSEARCH_*` environment variables.
* Bad, because OpenSearch Dashboards Dev Tools are not included in v1 (learners can still send `curl` requests against `host: true` or inside the lab, and inspect indices in Breadboard's Web UI and MCP).
* Bad, because OpenSearch's JVM + security index initialization takes ~15–25 seconds on cold start (covered by a 60-second `start_period` with 2-second `start_interval` probes).

### Confirmation

Unit tests verify `config.indices` validation, generated `opensearch.yml` and `start.sh`, TLS-only listener enforcement, and env contracts. Integration tests start a real `opensearchproject/opensearch:3.10.0` container, prove HTTPS TLS verification against the lab CA, confirm transport `9300` is unreachable from other containers, prove `host: true` routing, exercise `OpenSearchIndices`, and verify all three `opensearch/*` starters end-to-end.

## More Information

Recorded in `docs/design/decisions.md` and `docs/design/catalog.md`. Tracks part of GitHub issue #108.
