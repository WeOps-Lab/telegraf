//go:generate ../../../tools/readme_config_includer/generator
package redis_exporter

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

type Redis_exporter struct {
	Ok  bool            `toml:"ok"`
	Log telegraf.Logger `toml:"-"`
}

var client *redis.Client

func (*Redis_exporter) SampleConfig() string {
	return sampleConfig
}

// Init is for setup, and validating config.
func (r *Redis_exporter) Init() error {
	return nil
}

func (r *Redis_exporter) Gather(acc telegraf.Accumulator) error {
	r.connect()
	info, err := client.Info(context.Background(), "server").Result()
	if err != nil {
		acc.AddError(fmt.Errorf("获取info失败: %w", err))
		return err
	}

	fields := make(map[string]interface{})
	tags := map[string]string{
		"addr": client.Options().Addr,
	}

	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "uptime_in_seconds:") {
			var uptime int64
			if _, err := fmt.Sscanf(line, "uptime_in_seconds:%d", &uptime); err == nil {
				fields["uptime_in_seconds"] = uptime
			}
			break
		}
	}

	if len(fields) > 0 {
		acc.AddGauge("redis_exporter", fields, tags)
	}

	return nil
}

func (r *Redis_exporter) connect() {
	if client != nil {
		return
	}
	client = redis.NewClient(
		&redis.Options{
			Addr:     "127.0.0.1:6379",
			Password: "123456",
			PoolSize: 1,
		},
	)
}

func init() {
	inputs.Add("redis_exporter", func() telegraf.Input { return &Redis_exporter{} })
}
