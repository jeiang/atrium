package serverops

import (
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyHosts            = "ops_hosts"
	KeyAlertLabel       = "ops_alert_label"
	KeyQueryUp          = "ops_query_up"
	KeyQueryCPU         = "ops_query_cpu"
	KeyQueryMemory      = "ops_query_mem"
	KeyQueryDisk        = "ops_query_disk"
	KeyQueryFailedUnit  = "ops_query_failed_units"
	KeyWorkerMonthlyUSD = "ops_worker_monthly_usd"
	KeyWorkerMaxRunUSD  = "ops_worker_max_run_usd"
)

const (
	MetricUp          = "up"
	MetricCPU         = "cpu_pct"
	MetricMemory      = "mem_pct"
	MetricDisk        = "disk_root_pct"
	MetricFailedUnits = "failed_units"
	MetricAlerts      = "alerts_firing"
)

var Metrics = []string{MetricUp, MetricCPU, MetricMemory, MetricDisk, MetricFailedUnits, MetricAlerts}

const (
	defaultQueryUp         = `up{job="node",instance=~"{{host}}(:.*)?"}`
	defaultQueryCPU        = `100 - (avg by(instance) (rate(node_cpu_seconds_total{mode="idle",job="node",instance=~"{{host}}(:.*)?"}[5m])) * 100)`
	defaultQueryMemory     = `(1 - node_memory_MemAvailable_bytes{job="node",instance=~"{{host}}(:.*)?"} / node_memory_MemTotal_bytes{job="node",instance=~"{{host}}(:.*)?"}) * 100`
	defaultQueryDisk       = `(1 - node_filesystem_avail_bytes{job="node",instance=~"{{host}}(:.*)?" ,mountpoint="/"} / node_filesystem_size_bytes{job="node",instance=~"{{host}}(:.*)?" ,mountpoint="/"}) * 100`
	defaultQueryFailedUnit = `sum by(instance) (node_systemd_units{state="failed",job="node",instance=~"{{host}}(:.*)?"})`
)

func init() {
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{
			Key: KeyHosts, Module: "grafana", Type: settings.String,
		}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyWorkerMonthlyUSD, Module: "assistant", Type: settings.Float, Default: "5",
			Check: settings.Between(0, 10000),
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyWorkerMaxRunUSD, Module: "assistant", Type: settings.Float, Default: "0.10",
			Check: settings.Between(0, 10000),
		}),
	)
}
