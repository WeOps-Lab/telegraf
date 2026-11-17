//go:generate ../../../tools/readme_config_includer/generator
package bklite_rocketmq

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/filter"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

const defaultResponseTimeout = 10 * time.Second

// RocketMQ collects RocketMQ 4.9.x (admin protocol 4.4–4.9) via NameServer.
// RocketMQ 5.x native Prometheus metrics are a separate pull path.
type RocketMQ struct {
	NamesrvAddrs    []string        `toml:"namesrv_addrs"`
	BrokerAddr      string          `toml:"broker_addr"`
	AccessKey       string          `toml:"access_key"`
	SecretKey       string          `toml:"secret_key"`
	ResponseTimeout config.Duration `toml:"response_timeout"`
	TopicInclude    []string        `toml:"topic_include"`
	TopicExclude    []string        `toml:"topic_exclude"`
	GroupInclude    []string        `toml:"group_include"`
	GroupExclude    []string        `toml:"group_exclude"`
	Log             telegraf.Logger `toml:"-"`

	topicFilter filter.Filter
	groupFilter filter.Filter
	source      statsSource
}

type statsSource interface {
	Collect(ctx context.Context) (snapshot, error)
}

func (*RocketMQ) SampleConfig() string {
	return sampleConfig
}

func (r *RocketMQ) Init() error {
	if len(r.NamesrvAddrs) == 0 {
		return errors.New("namesrv_addrs is required")
	}
	if r.ResponseTimeout <= 0 {
		r.ResponseTimeout = config.Duration(defaultResponseTimeout)
	}
	var err error
	r.topicFilter, err = filter.NewIncludeExcludeFilter(r.TopicInclude, r.TopicExclude)
	if err != nil {
		return err
	}
	r.groupFilter, err = filter.NewIncludeExcludeFilter(r.GroupInclude, r.GroupExclude)
	if err != nil {
		return err
	}
	if r.source == nil {
		r.source, err = newMQAdmin(r.NamesrvAddrs, r.BrokerAddr, r.AccessKey, r.SecretKey)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *RocketMQ) Gather(acc telegraf.Accumulator) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.ResponseTimeout))
	defer cancel()

	snap, err := r.source.Collect(ctx)
	if err != nil {
		if r.Log != nil {
			r.Log.Errorf("rocketmq collect failed: %v", err)
		}
		acc.AddError(err)
		if snap.Namesrv == "" {
			acc.AddGauge("bklite_rocketmq_up", map[string]interface{}{"up": int64(0)}, map[string]string{
				"namesrv": r.namesrvTag(),
			})
			return nil
		}
	}
	r.emit(acc, snap)
	return nil
}

func (r *RocketMQ) namesrvTag() string {
	return strings.Join(r.NamesrvAddrs, ",")
}

func (r *RocketMQ) allowTopic(name string) bool {
	if r.topicFilter == nil {
		return true
	}
	return r.topicFilter.Match(name)
}

func (r *RocketMQ) allowGroup(name string) bool {
	if r.groupFilter == nil {
		return true
	}
	return r.groupFilter.Match(name)
}

func init() {
	inputs.Add("bklite_rocketmq", func() telegraf.Input {
		return &RocketMQ{}
	})
}
