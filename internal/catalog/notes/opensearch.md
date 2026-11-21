## What you got

A single-node OpenSearch 3.10 server with the Security plugin enabled and TLS on both the REST (`9200`) and internal transport (`9300`) layers:

- **HTTPS REST API on `9200`** using the service's lab certificate (`/etc/bb/tls/tls.crt`, `/etc/bb/tls/tls.key`) verified against `/etc/bb/ca.pem`. Add `host: true` to reach `https://<name>.<experiment>.localhost:<port>` from your host.
- **Loopback-only transport (`127.0.0.1:9300`)** so no other container can reach inter-node transport.
- **Generated `admin` password** stored in `.bb/secrets.json` and hashed into `internal_users.yml` at startup (all default demo certificates and demo users are removed).
- **Pre-created indices** via `config.indices` (comma-separated, optional), created with 1 primary shard and 0 replicas for single-node green health.
- **Bounded JVM heap** (`-Xms512m -Xmx512m`) inside a `1536m` container limit with Performance Analyzer disabled.

## Key concepts

- **Inverted index & analysis:** `text` fields are tokenized and lowercased for full-text BM25 search; `keyword` fields are stored verbatim for exact matches, sorting, and aggregations.
- **Query vs filter context:** Clauses in `must` compute relevance scores (`_score`); clauses in `filter` are binary yes/no and cached by Lucene.
- **Near-real-time refresh:** Indexed documents become searchable after a shard refresh (default 1 second) or an explicit `refresh=wait_for`.
- **Single-node shards:** With 1 node, an index with `number_of_replicas: 1` stays `yellow` because a replica shard cannot live on the same node as its primary; setting `number_of_replicas: 0` turns it `green`.
- **Aliases for migrations:** Index mappings are immutable once written; production changes write a new index, copy documents with `_reindex`, and swap an alias atomically.

## Try this

- Inspect cluster health and indices from inside the container:

  ```sh
  docker exec -it <container> sh -c 'curl -s --cacert /etc/bb/ca.pem -u "admin:$OPENSEARCH_PASSWORD" https://<service>.bb.internal:9200/_cat/indices?v'
  ```

- Test how an analyzer tokenizes text using the `_analyze` endpoint:

  ```sh
  docker exec -it <container> sh -c 'curl -s --cacert /etc/bb/ca.pem -u "admin:$OPENSEARCH_PASSWORD" -H "Content-Type: application/json" -d "{\"analyzer\":\"standard\",\"text\":\"Tesla Model 3 Long-Range\"}" https://<service>.bb.internal:9200/_analyze'
  ```

- Pre-create indices when adding OpenSearch:

  ```sh
  bb add opensearch --config indices=vehicles,events
  ```

## Break this

- **Skip explicit mappings:** Index a document whose numeric field arrives as a string first, then send an integer — watch dynamic mapping lock the type and reject or mis-sort later documents.
- **Leave default replicas on a single node:** Create an index with `number_of_replicas: 1` and check `_cluster/health` — notice the cluster stays `yellow` with unassigned replica shards.
- **Stop OpenSearch during a Kafka projection:** Run `docker stop <opensearch-container>` in `opensearch/postgres-cdc-sync` while `catalog-writer` keeps writing to Postgres and Kafka, then start OpenSearch again and watch `search-projector` catch up without losing records.
