# BKLite RocketMQ Input Plugin

`bklite_rocketmq` collects RocketMQ broker, producer, consumer, and consumer-group metrics through the NameServer admin protocol.

## Versions

The target is **RocketMQ 4.9.x** (4.9.4 through 4.9.8). That is the current 4.x line still running in production. The admin calls are the same remoting protocol used since 4.4, when ACL was added, so 4.4–4.8 clusters that speak this protocol also match.

RocketMQ **5.x** (current release 5.5.1) is a separate line. Since 5.1 it exposes its own Prometheus `/metrics`. This plugin does not collect that endpoint. A later BK-Lite pull path can select it; the measurement names here stay `bklite_rocketmq_*` so the two paths can share one metric contract.

`rocketmq-client-go` v2.1.2 can list topics from NameServer. It does not implement consume-stats, broker-runtime, or cluster-info queries. Those numeric fields are emitted when a stats source provides them, and are left absent otherwise. This plugin does not invent values and does not shell out to `mqadmin`.

## Configuration

```toml @sample.conf
# RocketMQ 4.9.x admin collection. Nameserver protocol matches 4.4 through 4.9.
# RocketMQ 5.x native /metrics is not collected by this plugin.
[[inputs.bklite_rocketmq]]
  ## NameServer addresses, host:port.
  namesrv_addrs = ["127.0.0.1:9876"]

  ## Optional ACL. Leave empty when the cluster has ACL disabled.
  # access_key = ""
  # secret_key = ""

  ## Admin call timeout.
  # response_timeout = "10s"

  ## Glob filters. Empty include means all. Used to bound topic and group cardinality.
  # topic_include = []
  # topic_exclude = []
  # group_include = []
  # group_exclude = []
```

## Metrics

| Measurement | Fields | Tags |
|---|---|---|
| `bklite_rocketmq_up` | `up` | `namesrv` |
| `bklite_rocketmq_broker` | `tps`, `qps`, `commitlog_disk_ratio` | `cluster`, `broker`, `broker_ip` |
| `bklite_rocketmq_producer` | `tps`, `message_size`, `offset` | `cluster`, `broker`, `topic` |
| `bklite_rocketmq_consumer` | `tps`, `message_size`, `offset` | `cluster`, `broker`, `group`, `topic` |
| `bklite_rocketmq_group` | `diff`, `ready`, `inflight`, `latency_ms`, `retry_diff`, `dlq` | `cluster`, `group`, `topic` |

`ready` is messages waiting to be consumed. `inflight` is messages pulled but not acked. When 4.x admin returns only one backlog, it is stored in `diff` and `ready` / `inflight` are omitted. A failed collection sets `up` to 0 and does not fail the Telegraf agent.
