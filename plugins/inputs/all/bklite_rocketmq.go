//go:build !custom || inputs || inputs.bklite_rocketmq

package all

import _ "github.com/influxdata/telegraf/plugins/inputs/bklite_rocketmq" // register plugin
