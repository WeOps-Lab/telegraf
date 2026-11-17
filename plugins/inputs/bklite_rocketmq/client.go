package bklite_rocketmq

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// mqAdmin speaks the RocketMQ 4.x admin remoting protocol.
// brokerAddr overrides the address registered in NameServer. On a laptop that
// address is usually a cluster DNS name, so local collection points it at a
// port-forwarded broker.
type mqAdmin struct {
	namesrv    []string
	namesrvTag string
	brokerAddr string
}

func newMQAdmin(addrs []string, brokerAddr, _, _ string) (*mqAdmin, error) {
	return &mqAdmin{
		namesrv:    addrs,
		namesrvTag: joinAddrs(addrs),
		brokerAddr: brokerAddr,
	}, nil
}

func (m *mqAdmin) Collect(ctx context.Context) (snapshot, error) {
	snap := snapshot{Namesrv: m.namesrvTag}
	body, namesrv, err := m.invokeNamesrv(ctx, codeGetBrokerClusterInfo, nil)
	if err != nil {
		return snapshot{}, fmt.Errorf("broker cluster info: %w", err)
	}
	var cluster clusterInfo
	if err = json.Unmarshal(relaxRocketMQJSON(body), &cluster); err != nil {
		return snapshot{}, fmt.Errorf("broker cluster info decode: %w", err)
	}
	brokers := cluster.masters()
	if m.brokerAddr != "" {
		for i := range brokers {
			brokers[i].dial = m.brokerAddr
		}
	}

	topicsBody, _, err := m.invokeNamesrv(ctx, codeGetAllTopicList, nil)
	if err != nil {
		return snap, fmt.Errorf("topic list: %w", err)
	}
	var topics topicList
	if err = json.Unmarshal(topicsBody, &topics); err != nil {
		return snap, fmt.Errorf("topic list decode: %w", err)
	}
	_ = namesrv

	var firstErr error
	for _, broker := range brokers {
		runtimeBody, err := invoke(ctx, broker.dial, codeGetBrokerRuntimeInfo, nil)
		if err != nil {
			firstErr = fmt.Errorf("broker runtime %s: %w", broker.dial, err)
			continue
		}
		var kv kvTable
		if err = json.Unmarshal(runtimeBody, &kv); err != nil {
			firstErr = err
			continue
		}
		snap.Brokers = append(snap.Brokers, brokerSample{
			Cluster:            broker.cluster,
			Broker:             broker.name,
			BrokerIP:           broker.registered,
			TPS:                firstTPS(kv.Table["putTps"]),
			QPS:                firstTPS(firstNonEmpty(kv.Table["getTotalTps"], kv.Table["getTransferredTps"], kv.Table["getFoundTps"])),
			CommitLogDiskRatio: parseFloat(kv.Table["commitLogDiskRatio"]),
		})
		snap.Producers = append(snap.Producers, m.topicOffsets(ctx, broker, topics.TopicList)...)
		groups, consumers := m.groupStats(ctx, broker)
		snap.Groups = append(snap.Groups, groups...)
		snap.Consumers = append(snap.Consumers, consumers...)
	}
	if len(snap.Brokers) == 0 && firstErr != nil {
		return snap, firstErr
	}
	return snap, nil
}

func (m *mqAdmin) topicOffsets(ctx context.Context, broker brokerEndpoint, topics []string) []producerSample {
	var out []producerSample
	for _, topic := range topics {
		if topic == "" || strings.HasPrefix(topic, "%") {
			continue
		}
		body, err := invoke(ctx, broker.dial, codeGetTopicStatsInfo, map[string]string{"topic": topic})
		if err != nil {
			continue
		}
		offset, ok := sumMaxOffset(body)
		if !ok {
			continue
		}
		out = append(out, producerSample{
			Cluster: broker.cluster,
			Broker:  broker.name,
			Topic:   topic,
			Offset:  &offset,
		})
	}
	return out
}

func (m *mqAdmin) groupStats(ctx context.Context, broker brokerEndpoint) ([]groupSample, []consumerSample) {
	body, err := invoke(ctx, broker.dial, codeGetAllSubscriptionGroup, nil)
	if err != nil {
		return nil, nil
	}
	var groups subscriptionGroups
	if err = json.Unmarshal(body, &groups); err != nil {
		return nil, nil
	}
	var groupOut []groupSample
	var consumerOut []consumerSample
	for name := range groups.SubscriptionGroupTable {
		statsBody, err := invoke(ctx, broker.dial, codeGetConsumeStats, map[string]string{"consumerGroup": name})
		if err != nil {
			continue
		}
		queues, groupTPS := parseConsumeQueues(statsBody)
		perTopicTPS := m.perTopicConsumeTPS(ctx, broker, name, queues, groupTPS)
		samples, consumers := buildConsumeSamples(broker.cluster, broker.name, name, queues, perTopicTPS)
		groupOut = append(groupOut, samples...)
		consumerOut = append(consumerOut, consumers...)
		dlq, hasDLQ := m.dlqDepth(ctx, broker, name)
		if !hasDLQ {
			continue
		}
		if len(samples) == 0 {
			groupOut = append(groupOut, dlqGroupSample(broker.cluster, name, dlq))
			continue
		}
		groupOut = append(groupOut, dlqGroupSample(broker.cluster, name, dlq))
	}
	return groupOut, consumerOut
}

func (m *mqAdmin) perTopicConsumeTPS(ctx context.Context, broker brokerEndpoint, group string, queues []consumeQueue, groupTPS *float64) map[string]*float64 {
	topics := consumeTopics(queues)
	if len(topics) == 1 {
		if groupTPS == nil {
			return nil
		}
		return map[string]*float64{topics[0]: groupTPS}
	}
	if len(topics) < 2 {
		return nil
	}
	out := map[string]*float64{}
	for _, topic := range topics {
		body, err := invoke(ctx, broker.dial, codeGetConsumeStats, map[string]string{
			"consumerGroup": group,
			"topic":         topic,
		})
		if err != nil {
			continue
		}
		_, tps := parseConsumeQueues(body)
		if tps != nil {
			out[topic] = tps
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *mqAdmin) dlqDepth(ctx context.Context, broker brokerEndpoint, group string) (float64, bool) {
	body, err := invoke(ctx, broker.dial, codeGetTopicStatsInfo, map[string]string{"topic": "%DLQ%" + group})
	if err != nil {
		return 0, false
	}
	return sumMaxOffset(body)
}

func (m *mqAdmin) invokeNamesrv(ctx context.Context, code int, ext map[string]string) ([]byte, string, error) {
	var last error
	for _, addr := range m.namesrv {
		body, err := invoke(ctx, addr, code, ext)
		if err == nil {
			return body, addr, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no nameserver address")
	}
	return nil, "", last
}

type brokerEndpoint struct {
	cluster    string
	name       string
	registered string
	dial       string
}

type clusterInfo struct {
	BrokerAddrTable map[string]struct {
		Cluster     string            `json:"cluster"`
		BrokerName  string            `json:"brokerName"`
		BrokerAddrs map[string]string `json:"brokerAddrs"`
	} `json:"brokerAddrTable"`
}

func (c clusterInfo) masters() []brokerEndpoint {
	var out []brokerEndpoint
	for _, broker := range c.BrokerAddrTable {
		addr := broker.BrokerAddrs["0"]
		if addr == "" {
			continue
		}
		name := broker.BrokerName
		if name == "" {
			name = addr
		}
		out = append(out, brokerEndpoint{
			cluster:    broker.Cluster,
			name:       name,
			registered: addr,
			dial:       addr,
		})
	}
	return out
}

type kvTable struct {
	Table map[string]string `json:"table"`
}

type topicList struct {
	TopicList []string `json:"topicList"`
}

type subscriptionGroups struct {
	SubscriptionGroupTable map[string]json.RawMessage `json:"subscriptionGroupTable"`
}

var maxOffsetPattern = regexp.MustCompile(`"maxOffset"\s*:\s*(-?\d+)`)

func sumMaxOffset(body []byte) (float64, bool) {
	matches := maxOffsetPattern.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return 0, false
	}
	var sum float64
	for _, match := range matches {
		value, err := strconv.ParseFloat(string(match[1]), 64)
		if err != nil {
			continue
		}
		sum += value
	}
	return sum, true
}

type consumeQueue struct {
	topic          string
	brokerOffset   *float64
	consumerOffset *float64
}

func parseConsumeQueues(body []byte) ([]consumeQueue, *float64) {
	tps, _ := namedFloat(body, "consumeTps")
	text := string(body)
	var queues []consumeQueue
	for i := 0; i < len(text); {
		rel := strings.Index(text[i:], `"brokerName"`)
		if rel < 0 {
			break
		}
		at := i + rel
		start := strings.LastIndex(text[:at], "{")
		if start < 0 {
			i = at + len(`"brokerName"`)
			continue
		}
		keyEnd := matchBrace(text, start)
		if keyEnd < 0 {
			i = at + len(`"brokerName"`)
			continue
		}
		rest := strings.TrimLeft(text[keyEnd+1:], " \t\r\n")
		if !strings.HasPrefix(rest, ":") {
			i = keyEnd + 1
			continue
		}
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
		if !strings.HasPrefix(rest, "{") {
			i = keyEnd + 1
			continue
		}
		valueEnd := matchBrace(rest, 0)
		if valueEnd < 0 {
			break
		}
		key := text[start : keyEnd+1]
		value := rest[:valueEnd+1]
		topic := namedString(key, "topic")
		brokerOffset, hasBroker := namedFloat([]byte(value), "brokerOffset")
		consumerOffset, hasConsumer := namedFloat([]byte(value), "consumerOffset")
		if topic != "" && (hasBroker || hasConsumer) {
			queues = append(queues, consumeQueue{
				topic:          topic,
				brokerOffset:   brokerOffset,
				consumerOffset: consumerOffset,
			})
		}
		i = (len(text) - len(rest)) + valueEnd + 1
	}
	return queues, tps
}

func buildConsumeSamples(cluster, broker, group string, queues []consumeQueue, topicTPS map[string]*float64) ([]groupSample, []consumerSample) {
	type agg struct {
		diff    float64
		hasDiff bool
		offset  float64
		hasOff  bool
	}
	byTopic := map[string]*agg{}
	var topics []string
	for _, queue := range queues {
		if queue.topic == "" {
			continue
		}
		item := byTopic[queue.topic]
		if item == nil {
			item = &agg{}
			byTopic[queue.topic] = item
			topics = append(topics, queue.topic)
		}
		if queue.brokerOffset != nil && queue.consumerOffset != nil {
			gap := *queue.brokerOffset - *queue.consumerOffset
			if gap < 0 {
				gap = 0
			}
			item.diff += gap
			item.hasDiff = true
		}
		if queue.consumerOffset != nil {
			item.offset += *queue.consumerOffset
			item.hasOff = true
		}
	}
	var groups []groupSample
	var consumers []consumerSample
	for _, topic := range topics {
		item := byTopic[topic]
		if item.hasDiff {
			diff := item.diff
			groups = append(groups, groupSample{
				Cluster: cluster,
				Group:   group,
				Topic:   topic,
				Diff:    &diff,
			})
		}
		if !item.hasOff && topicTPS[topic] == nil {
			continue
		}
		sample := consumerSample{Cluster: cluster, Broker: broker, Group: group, Topic: topic}
		if item.hasOff {
			offset := item.offset
			sample.Offset = &offset
		}
		if tps := topicTPS[topic]; tps != nil {
			sample.TPS = tps
		}
		consumers = append(consumers, sample)
	}
	return groups, consumers
}

func consumeTopics(queues []consumeQueue) []string {
	seen := map[string]struct{}{}
	var topics []string
	for _, queue := range queues {
		if queue.topic == "" {
			continue
		}
		if _, ok := seen[queue.topic]; ok {
			continue
		}
		seen[queue.topic] = struct{}{}
		topics = append(topics, queue.topic)
	}
	return topics
}

func dlqGroupSample(cluster, group string, dlq float64) groupSample {
	return groupSample{
		Cluster: cluster,
		Group:   group,
		Topic:   "%DLQ%" + group,
		DLQ:     &dlq,
	}
}

func matchBrace(text string, start int) int {
	if start < 0 || start >= len(text) || text[start] != '{' {
		return -1
	}
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func namedString(text, name string) string {
	match := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*"([^"]*)"`).FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	return match[1]
}

func namedFloat(body []byte, name string) (*float64, bool) {
	match := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*(-?\d+(?:\.\d+)?)`).FindSubmatch(body)
	if match == nil {
		return nil, false
	}
	value, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		return nil, false
	}
	return &value, true
}

func firstTPS(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return parseFloat(strings.Fields(raw)[0])
}

func parseFloat(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func joinAddrs(addrs []string) string {
	return strings.Join(addrs, ",")
}

// RocketMQ serializes integer map keys without quotes, which encoding/json rejects.
var unquotedKey = regexp.MustCompile(`([{\[,])(\d+):`)

func relaxRocketMQJSON(body []byte) []byte {
	return unquotedKey.ReplaceAll(body, []byte(`$1"$2":`))
}
