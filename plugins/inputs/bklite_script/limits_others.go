//go:build unix && !linux

package bklite_script

import "github.com/influxdata/telegraf"

func applyResourceLimits(int, int64, int, telegraf.Logger) func() {
	return nil
}
