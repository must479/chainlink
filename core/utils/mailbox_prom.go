package utils

import (
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var mailboxLoad = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "mailbox_load_percent",
	Help: "Percent of mailbox capacity used",
},
	[]string{"name", "capacity"},
)

const mailboxPromInterval = 10 * time.Second

var (
	mailboxes   sync.Map
	monitorOnce sync.Once
)

type loadFn func() (capacity uint64, percent float64)

func monitor(name string, load loadFn) {
	mailboxes.Store(name, load)
	monitorOnce.Do(startMonitoring)
}

func unMonitor(name string) {
	mailboxes.Delete(name)
}

func startMonitoring() {
	go func() {
		for range time.Tick(mailboxPromInterval) {
			mailboxes.Range(func(k, v any) bool {
				name, load := k.(string), v.(loadFn)
				c, p := load()
				capacity := strconv.FormatUint(c, 10)
				mailboxLoad.WithLabelValues(name, capacity).Set(p)
				return true
			})
		}
	}()
}
