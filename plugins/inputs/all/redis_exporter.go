//go:build !custom || inputs || inputs.redis_exporter

package all

import _ "github.com/influxdata/telegraf/plugins/inputs/redis_exporter" // register plugin
