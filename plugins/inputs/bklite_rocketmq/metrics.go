package bklite_rocketmq

import "github.com/influxdata/telegraf"

// snapshot is one admin collection. Nil numeric fields are omitted.
// RocketMQ 4.x admin often returns a single backlog in Diff and does not
// split Ready and Inflight; those stay nil instead of being invented.
type snapshot struct {
	Namesrv   string
	Brokers   []brokerSample
	Producers []producerSample
	Consumers []consumerSample
	Groups    []groupSample
}

type brokerSample struct {
	Cluster            string
	Broker             string
	BrokerIP           string
	TPS                *float64
	QPS                *float64
	CommitLogDiskRatio *float64
}

type producerSample struct {
	Cluster     string
	Broker      string
	Topic       string
	TPS         *float64
	MessageSize *float64
	Offset      *float64
}

type consumerSample struct {
	Cluster     string
	Broker      string
	Group       string
	Topic       string
	TPS         *float64
	MessageSize *float64
	Offset      *float64
}

type groupSample struct {
	Cluster   string
	Group     string
	Topic     string
	Diff      *float64
	Ready     *float64
	Inflight  *float64
	LatencyMs *float64
	RetryDiff *float64
	DLQ       *float64
}

func (r *RocketMQ) emit(acc telegraf.Accumulator, snap snapshot) {
	namesrv := snap.Namesrv
	if namesrv == "" {
		namesrv = r.namesrvTag()
	}
	acc.AddGauge("bklite_rocketmq_up", map[string]interface{}{"up": int64(1)}, map[string]string{"namesrv": namesrv})

	for _, broker := range snap.Brokers {
		fields := map[string]interface{}{}
		addOptional(fields, "tps", broker.TPS)
		addOptional(fields, "qps", broker.QPS)
		addOptional(fields, "commitlog_disk_ratio", broker.CommitLogDiskRatio)
		if len(fields) == 0 {
			continue
		}
		acc.AddGauge("bklite_rocketmq_broker", fields, map[string]string{
			"cluster":   broker.Cluster,
			"broker":    broker.Broker,
			"broker_ip": broker.BrokerIP,
		})
	}

	for _, producer := range snap.Producers {
		if !r.allowTopic(producer.Topic) {
			continue
		}
		fields := map[string]interface{}{}
		addOptional(fields, "tps", producer.TPS)
		addOptional(fields, "message_size", producer.MessageSize)
		addOptional(fields, "offset", producer.Offset)
		if len(fields) == 0 {
			continue
		}
		acc.AddGauge("bklite_rocketmq_producer", fields, map[string]string{
			"cluster": producer.Cluster,
			"broker":  producer.Broker,
			"topic":   producer.Topic,
		})
	}

	for _, consumer := range snap.Consumers {
		if !r.allowTopic(consumer.Topic) || !r.allowGroup(consumer.Group) {
			continue
		}
		fields := map[string]interface{}{}
		addOptional(fields, "tps", consumer.TPS)
		addOptional(fields, "message_size", consumer.MessageSize)
		addOptional(fields, "offset", consumer.Offset)
		if len(fields) == 0 {
			continue
		}
		acc.AddGauge("bklite_rocketmq_consumer", fields, map[string]string{
			"cluster": consumer.Cluster,
			"broker":  consumer.Broker,
			"group":   consumer.Group,
			"topic":   consumer.Topic,
		})
	}

	for _, group := range snap.Groups {
		if !r.allowTopic(group.Topic) || !r.allowGroup(group.Group) {
			continue
		}
		fields := map[string]interface{}{}
		addOptional(fields, "diff", group.Diff)
		addOptional(fields, "ready", group.Ready)
		addOptional(fields, "inflight", group.Inflight)
		addOptional(fields, "latency_ms", group.LatencyMs)
		addOptional(fields, "retry_diff", group.RetryDiff)
		addOptional(fields, "dlq", group.DLQ)
		if len(fields) == 0 {
			continue
		}
		acc.AddGauge("bklite_rocketmq_group", fields, map[string]string{
			"cluster": group.Cluster,
			"group":   group.Group,
			"topic":   group.Topic,
		})
	}
}

func addOptional(fields map[string]interface{}, name string, value *float64) {
	if value != nil {
		fields[name] = *value
	}
}
