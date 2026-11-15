---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Use SeaweedFS for the S3 Compatible catalog entry

## Context and Problem Statement

Breadboard's catalog needs an object store that speaks the S3 API. MinIO was the obvious choice, but its community edition stopped publishing binaries and images in October 2025, entered maintenance mode in December 2025 and was archived in 2026, and its images have disappeared from Docker Hub. Which S3-compatible server should the catalog entry use, and how should it be presented?

## Decision Drivers

* A maintained, real server with official container images.
* A license that permits running it unmodified in a local container.
* Single-container operation and enough S3 coverage for buckets, multipart uploads and presigned URLs.
* Presented as generic S3-compatible storage, not branded as AWS S3.

## Considered Options

* SeaweedFS
* Garage
* RustFS
* MinIO community edition
* LocalStack

## Decision Outcome

Chosen option: "SeaweedFS", because it is mature (since 2015), Apache-2.0 licensed, written in Go, and runs as a single container with `weed server -s3`.

The entry is named **S3 Compatible** (`s3-compatible`). Its env contract provides generic `S3_*` variables plus the `AWS_*` variables official AWS SDKs read, so clients such as `aws-sdk-go-v2` connect with no code changes. The exact image tag and TLS configuration are verified when the entry is built in Phase 2.

### Consequences

* Good, because the entry is backed by a maintained server under a permissive license.
* Good, because exports map cleanly to Amazon S3, Google Cloud Storage and Cloudflare R2.
* Bad, because SeaweedFS's TLS setup for the S3 gateway must be verified and may need extra configuration.
* Bad, because S3 API coverage differs between servers; starters must stay within the common subset.

### Confirmation

The catalog entry's health check makes an authenticated S3 request over verified TLS. The `s3/objects-basics` starter's integration test exercises bucket creation, multipart upload and presigned URLs against it.

## Pros and Cons of the Options

### SeaweedFS

* Good, because it is mature, Apache-2.0 and single-container.
* Neutral, because it also offers filesystem and other APIs Breadboard does not need.
* Bad, because its minimum memory footprint is higher than Garage's.

### Garage

* Good, because it is lightweight and actively maintained.
* Neutral, because AGPL-3.0 is fine for running unmodified but matters if modified.
* Bad, because it needs a cluster-layout bootstrap step and omits bucket policies, ACLs and versioning.

### RustFS

* Good, because it is Apache-2.0 and MinIO-like.
* Bad, because sources disagree on whether it has left alpha.

### MinIO community edition

* Good, because it is widely known.
* Bad, because it is archived and no longer publishes images or security fixes.

### LocalStack

* Good, because it emulates many AWS services.
* Bad, because it is an emulator, which Breadboard excludes (ADR-0003, D-001).

## More Information

Recorded as D-023 in `docs/design/decisions.md`. The catalog entry is described in `docs/design/catalog.md`.
