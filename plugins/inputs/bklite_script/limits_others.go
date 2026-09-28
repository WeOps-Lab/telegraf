//go:build unix && !linux

package bklite_script

import (
	"time"

	"github.com/influxdata/telegraf"
)

func applyResourceLimits(int, time.Duration, telegraf.Logger) func() {
	return nil
}
