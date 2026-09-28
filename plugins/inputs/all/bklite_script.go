//go:build !custom || inputs || inputs.bklite_script

package all

import _ "github.com/influxdata/telegraf/plugins/inputs/bklite_script" // register plugin
