# Kafka

## What you got

One Apache Kafka broker in KRaft mode: the same process is the broker and the controller, so there is no ZooKeeper. Clients connect over SSL at `kafka.bb.internal:9093` and verify the broker's certificate against the Breadboard CA. Topics are never created automatically; your code creates them with the admin client.

Replication factor is 1. If this container stops, every partition is unavailable until it returns.

## Key concepts

- **Topic and partition.** A topic is split into partitions. Order is guaranteed only within a partition, and the record key picks the partition.
- **Consumer group.** Consumers sharing a `group.id` split the partitions between them. More consumers than partitions leaves some idle.
- **Offset.** Each group stores how far it has read in each partition. Committing after processing gives at-least-once delivery.
- **Rebalance.** When a consumer joins or leaves, the group reassigns partitions. Processing pauses while it happens.

## Try this

- Create a topic with three partitions, then run two consumers in the same group and watch the partitions split.
- Produce records with the same key and confirm they always land on the same partition.
- Start a consumer in a new group and read the topic from the beginning.

## Break this

- `docker stop bb-<experiment>-kafka-1` while the producer is writing. Watch the client retry, then restart it with `bb up`.
- Kill a consumer before it commits. Restart it and count the records it processes twice.
- Give the broker `resources.memory: 512m` and produce large records until it struggles.
