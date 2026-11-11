package serverops

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyHosts            = "ops_hosts"
	KeyLabelSelector    = "ops_label_selector"
	KeyQueryUp          = "ops_query_up"
	KeyQueryCPU         = "ops_query_cpu"
	KeyQueryMemory      = "ops_query_mem"
	KeyQueryDisk        = "ops_query_disk"
	KeyQueryFailedUnit  = "ops_query_failed_units"
	KeyWorkerMonthlyUSD = "ops_worker_monthly_usd"
	KeyWorkerMaxRunUSD  = "ops_worker_max_run_usd"
	KeyCondenser        = "ops_condenser"
	KeyCondenserModel   = "ops_condenser_model"
)

const (
	CondenserHaiku = "haiku"
	CondenserLocal = "local"
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
	defaultLabelSelector   = `job="node"`
	defaultQueryUp         = `up{{{selector}},instance=~"{{host}}(:.*)?"}`
	defaultQueryCPU        = `100 - (avg by(instance) (rate(node_cpu_seconds_total{mode="idle",{{selector}},instance=~"{{host}}(:.*)?"}[5m])) * 100)`
	defaultQueryMemory     = `(1 - node_memory_MemAvailable_bytes{{{selector}},instance=~"{{host}}(:.*)?"} / node_memory_MemTotal_bytes{{{selector}},instance=~"{{host}}(:.*)?"}) * 100`
	defaultQueryDisk       = `(1 - node_filesystem_avail_bytes{{{selector}},instance=~"{{host}}(:.*)?" ,mountpoint="/"} / node_filesystem_size_bytes{{{selector}},instance=~"{{host}}(:.*)?" ,mountpoint="/"}) * 100`
	defaultQueryFailedUnit = `sum by(instance) (node_systemd_unit_state{state="failed",{{selector}},instance=~"{{host}}(:.*)?"} == 1)`
)

var (
	hostRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	selectorRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*(?:=|!=|=~|!~)\s*"(?:[^"\\{}\n\r]|\\.)*"` +
		`(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*\s*(?:=|!=|=~|!~)\s*"(?:[^"\\{}\n\r]|\\.)*")*$`)
)

func checkHosts(value string) error {
	for _, host := range SplitHosts(value) {
		if !hostRe.MatchString(host) {
			return fmt.Errorf("host %q must be letters, digits, dots, dashes or underscores", host)
		}
	}
	return nil
}

func checkSelector(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("the label selector must hold at least one matcher")
	}
	if !selectorRe.MatchString(value) {
		return errors.New(`the label selector must be a comma-separated list of matchers like job="node"`)
	}
	return nil
}

func checkQuery(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("the query must not be empty")
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("the query must be a single line")
	}
	if !strings.Contains(value, "{{host}}") {
		return errors.New("the query must contain the {{host}} placeholder")
	}
	stripped := strings.ReplaceAll(value, "{{host}}", "")
	stripped = strings.ReplaceAll(stripped, "{{selector}}", "")
	if strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
		return errors.New("the query may only use the {{host}} and {{selector}} placeholders")
	}
	test := strings.ReplaceAll(value, "{{selector}}", defaultLabelSelector)
	test = strings.ReplaceAll(test, "{{host}}", "host")
	if strings.ContainsAny(test, "#;") {
		return errors.New("the query must be one PromQL expression without comments")
	}
	if !balanced(test) {
		return errors.New("the query has unbalanced braces, brackets, parentheses or quotes")
	}
	return nil
}

func balanced(expr string) bool {
	var braces, parens, brackets int
	quote, escaped := false, false
	for _, r := range expr {
		if quote {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				quote = false
			}
			continue
		}
		switch r {
		case '"':
			quote = true
		case '{':
			braces++
		case '}':
			braces--
			if braces < 0 {
				return false
			}
		case '(':
			parens++
		case ')':
			parens--
			if parens < 0 {
				return false
			}
		case '[':
			brackets++
		case ']':
			brackets--
			if brackets < 0 {
				return false
			}
		}
	}
	return !quote && braces == 0 && parens == 0 && brackets == 0
}

func init() {
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{
			Key: KeyHosts, Module: "grafana", Type: settings.String, Check: checkHosts,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyLabelSelector, Module: "grafana", Type: settings.String, Default: defaultLabelSelector, Check: checkSelector,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyQueryUp, Module: "grafana", Type: settings.String, Default: defaultQueryUp, Check: checkQuery,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyQueryCPU, Module: "grafana", Type: settings.String, Default: defaultQueryCPU, Check: checkQuery,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyQueryMemory, Module: "grafana", Type: settings.String, Default: defaultQueryMemory, Check: checkQuery,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyQueryDisk, Module: "grafana", Type: settings.String, Default: defaultQueryDisk, Check: checkQuery,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyQueryFailedUnit, Module: "grafana", Type: settings.String, Default: defaultQueryFailedUnit, Check: checkQuery,
		}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyWorkerMonthlyUSD, Label: "Server-ops monthly cap (USD)", Module: "assistant", Type: settings.Float, Default: "5",
			Check: settings.Between(0, 10000),
		}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyWorkerMaxRunUSD, Label: "Server-ops run cost cap (USD)", Module: "assistant", Type: settings.Float, Default: "0.10",
			Check: settings.Between(0, 10000),
		}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyCondenser, Label: "Server-ops findings written by", Module: "assistant", Type: settings.Enum,
			Choices: []string{CondenserHaiku, CondenserLocal}, Default: CondenserHaiku,
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyCondenserModel, Module: "assistant", Type: settings.String,
		}),
	)
}
