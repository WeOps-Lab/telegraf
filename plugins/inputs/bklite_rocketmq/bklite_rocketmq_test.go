package bklite_rocketmq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

func TestGatherEmitsOperationalMetricsAndSkipsMissingFields(t *testing.T) {
	plugin := &RocketMQ{
		NamesrvAddrs: []string{"10.0.0.1:9876"},
		source: fakeSource{snap: snapshot{
			Namesrv: "10.0.0.1:9876",
			Brokers: []brokerSample{{
				Cluster: "c1", Broker: "broker-a", BrokerIP: "10.0.0.2",
				TPS: f64(12), QPS: f64(30), CommitLogDiskRatio: f64(0.4),
			}},
			Producers: []producerSample{{
				Cluster: "c1", Broker: "broker-a", Topic: "orders",
				TPS: f64(8), MessageSize: f64(128), Offset: f64(1000),
			}},
			Consumers: []consumerSample{{
				Cluster: "c1", Broker: "broker-a", Group: "billing", Topic: "orders",
				TPS: f64(7), MessageSize: f64(128), Offset: f64(900),
			}},
			Groups: []groupSample{{
				Cluster: "c1", Group: "billing", Topic: "orders",
				Diff: f64(100), LatencyMs: f64(40), RetryDiff: f64(3), DLQ: f64(1),
			}},
		}},
	}
	require.NoError(t, plugin.Init())

	acc := &testutil.Accumulator{}
	require.NoError(t, plugin.Gather(acc))
	require.Empty(t, acc.Errors)

	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_up", map[string]interface{}{"up": int64(1)}, map[string]string{"namesrv": "10.0.0.1:9876"})
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_broker", map[string]interface{}{
		"tps": float64(12), "qps": float64(30), "commitlog_disk_ratio": float64(0.4),
	}, map[string]string{"cluster": "c1", "broker": "broker-a", "broker_ip": "10.0.0.2"})
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_group", map[string]interface{}{
		"diff": float64(100), "latency_ms": float64(40), "retry_diff": float64(3), "dlq": float64(1),
	}, map[string]string{"cluster": "c1", "group": "billing", "topic": "orders"})

	group, ok := acc.Get("bklite_rocketmq_group")
	require.True(t, ok)
	require.NotContains(t, group.Fields, "ready")
	require.NotContains(t, group.Fields, "inflight")
}

func TestGatherFiltersTopicAndGroup(t *testing.T) {
	plugin := &RocketMQ{
		NamesrvAddrs: []string{"10.0.0.1:9876"},
		TopicInclude: []string{"orders"},
		GroupExclude: []string{"billing"},
		source: fakeSource{snap: snapshot{
			Producers: []producerSample{
				{Cluster: "c1", Broker: "b", Topic: "orders", TPS: f64(1)},
				{Cluster: "c1", Broker: "b", Topic: "audit", TPS: f64(1)},
			},
			Groups: []groupSample{
				{Cluster: "c1", Group: "billing", Topic: "orders", Diff: f64(1)},
				{Cluster: "c1", Group: "search", Topic: "orders", Diff: f64(2)},
				{Cluster: "c1", Group: "search", Topic: "audit", Diff: f64(3)},
			},
		}},
	}
	require.NoError(t, plugin.Init())

	acc := &testutil.Accumulator{}
	require.NoError(t, plugin.Gather(acc))

	require.Len(t, acc.Metrics, 3)
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_producer", map[string]interface{}{"tps": float64(1)}, map[string]string{
		"cluster": "c1", "broker": "b", "topic": "orders",
	})
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_group", map[string]interface{}{"diff": float64(2)}, map[string]string{
		"cluster": "c1", "group": "search", "topic": "orders",
	})
}

func TestGatherFailureKeepsTelegrafUp(t *testing.T) {
	plugin := &RocketMQ{
		NamesrvAddrs:    []string{"10.0.0.1:9876"},
		ResponseTimeout: config.Duration(time.Second),
		source:          fakeSource{err: errors.New("nameserver down")},
	}
	require.NoError(t, plugin.Init())

	acc := &testutil.Accumulator{}
	require.NoError(t, plugin.Gather(acc))
	require.Len(t, acc.Errors, 1)
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_up", map[string]interface{}{"up": int64(0)}, map[string]string{"namesrv": "10.0.0.1:9876"})
}

func TestParseRuntimeAndTopicOffset(t *testing.T) {
	tps := firstTPS("1.5 0.4 0.1")
	require.NotNil(t, tps)
	require.Equal(t, 1.5, *tps)
	disk := parseFloat("0.84")
	require.NotNil(t, disk)
	require.Equal(t, 0.84, *disk)

	offset, ok := sumMaxOffset([]byte(`{"offsetTable":{"q0":{"maxOffset":4},"q1":{"maxOffset":6}}}`))
	require.True(t, ok)
	require.Equal(t, float64(10), offset)

	info := clusterInfo{BrokerAddrTable: map[string]struct {
		Cluster     string            `json:"cluster"`
		BrokerName  string            `json:"brokerName"`
		BrokerAddrs map[string]string `json:"brokerAddrs"`
	}{
		"broker-a": {Cluster: "DefaultCluster", BrokerName: "broker-a", BrokerAddrs: map[string]string{"0": "10.0.0.2:10911"}},
	}}
	masters := info.masters()
	require.Len(t, masters, 1)
	require.Equal(t, "10.0.0.2:10911", masters[0].dial)
}

func TestParse49ConsumeStatsObjectKeys(t *testing.T) {
	body := []byte(`{"consumeTps":1.2,"offsetTable":{{"brokerName":"broker-a","queueId":0,"topic":"weopsx_monitor_demo"}:{"brokerOffset":123,"consumerOffset":120,"lastTimestamp":1710000000000},{"topic":"weopsx_monitor_demo","queueId":1,"brokerName":"broker-a"}:{"lastTimestamp":1710000000001,"consumerOffset":10,"brokerOffset":15}}}`)
	queues, groupTPS := parseConsumeQueues(body)
	require.Len(t, queues, 2)
	require.NotNil(t, groupTPS)
	require.Equal(t, 1.2, *groupTPS)

	topics := consumeTopics(queues)
	require.Equal(t, []string{"weopsx_monitor_demo"}, topics)
	groups, consumers := buildConsumeSamples("DefaultCluster", "broker-a", "weopsx_monitor_cg", queues, map[string]*float64{
		topics[0]: groupTPS,
	})
	require.Len(t, groups, 1)
	require.Equal(t, "weopsx_monitor_demo", groups[0].Topic)
	require.Equal(t, float64(8), *groups[0].Diff)
	require.Nil(t, groups[0].Ready)
	require.Nil(t, groups[0].Inflight)
	require.Len(t, consumers, 1)
	require.Equal(t, float64(130), *consumers[0].Offset)
	require.Equal(t, 1.2, *consumers[0].TPS)
	require.Nil(t, consumers[0].MessageSize)

	plugin := &RocketMQ{NamesrvAddrs: []string{"10.0.0.1:9876"}}
	require.NoError(t, plugin.Init())
	acc := &testutil.Accumulator{}
	plugin.emit(acc, snapshot{
		Namesrv:   "10.0.0.1:9876",
		Groups:    groups,
		Consumers: consumers,
	})
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_group", map[string]interface{}{"diff": float64(8)}, map[string]string{
		"cluster": "DefaultCluster", "group": "weopsx_monitor_cg", "topic": "weopsx_monitor_demo",
	})
	acc.AssertContainsTaggedFields(t, "bklite_rocketmq_consumer", map[string]interface{}{
		"offset": float64(130), "tps": 1.2,
	}, map[string]string{
		"cluster": "DefaultCluster", "broker": "broker-a", "group": "weopsx_monitor_cg", "topic": "weopsx_monitor_demo",
	})
	got, ok := acc.Get("bklite_rocketmq_group")
	require.True(t, ok)
	require.NotContains(t, got.Fields, "ready")
	require.NotContains(t, got.Fields, "inflight")
}

func TestGroupTPSNotCopiedAcrossTopics(t *testing.T) {
	body := []byte(`{"consumeTps":9.9,"offsetTable":{{"brokerName":"broker-a","queueId":0,"topic":"orders"}:{"brokerOffset":5,"consumerOffset":1},{"brokerName":"broker-a","queueId":0,"topic":"audit"}:{"consumerOffset":4,"brokerOffset":4}}}`)
	queues, groupTPS := parseConsumeQueues(body)
	require.NotNil(t, groupTPS)
	require.Len(t, consumeTopics(queues), 2)
	_, consumers := buildConsumeSamples("DefaultCluster", "broker-a", "weopsx_monitor_cg", queues, nil)
	require.Len(t, consumers, 2)
	for _, consumer := range consumers {
		require.Nil(t, consumer.TPS)
		require.Nil(t, consumer.MessageSize)
	}
}

func TestDLQFallbackCarriesTopic(t *testing.T) {
	sample := dlqGroupSample("DefaultCluster", "weopsx_monitor_cg_fail", 4)
	require.Equal(t, "%DLQ%weopsx_monitor_cg_fail", sample.Topic)
	require.Equal(t, "weopsx_monitor_cg_fail", sample.Group)
	require.Equal(t, float64(4), *sample.DLQ)
	require.Nil(t, sample.Ready)
	require.Nil(t, sample.Inflight)
	require.Nil(t, sample.Diff)
}

func TestInitRequiresNamesrv(t *testing.T) {
	plugin := &RocketMQ{}
	require.Error(t, plugin.Init())
}

type fakeSource struct {
	snap snapshot
	err  error
}

func (f fakeSource) Collect(context.Context) (snapshot, error) {
	if f.err != nil {
		return snapshot{}, f.err
	}
	return f.snap, nil
}
