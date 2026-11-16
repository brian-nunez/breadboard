# S3 Compatible

## What you got

Object storage that speaks the S3 API, run by SeaweedFS in one container. The only network listener is HTTPS on port 8333, verified against the lab CA. Apps get an admin access key pair in both `S3_*` and `AWS_*` variables, so the official AWS SDKs connect with no endpoint code. Buckets listed in `config.buckets` are created at startup.

## Key concepts

- **Buckets and keys.** There are no folders: a key like `reports/2026/q4.csv` is one name, and listing with a prefix and a `/` delimiter only makes it look like a tree.
- **Path-style addressing.** Clients put the bucket in the path (`/bucket/key`) instead of the hostname, because the lab has no wildcard DNS for `bucket.s3-compatible.bb.internal`.
- **Multipart upload.** Large objects go up in parts that can be retried on their own, then a final call stitches them together.
- **Presigned URLs.** A signed, expiring URL lets someone without credentials download or upload one object.

## Try this

- Create a bucket, upload `a/1.txt` and `a/b/2.txt`, and list with prefix `a/` and delimiter `/`.
- Generate a presigned GET URL with a one-minute expiry and fetch it with `curl` before and after it expires.
- Add `buckets: reports,images` under `config` and run `bb up`.

## Break this

- Change one character of a presigned URL's signature and read the error.
- Start a multipart upload and never complete it; the parts take space until you abort it.
- Delete a bucket that still holds objects and read the `BucketNotEmpty` error.
