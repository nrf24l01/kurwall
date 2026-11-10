package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Validate rejects unsafe or incomplete agent configuration.
func Validate(cfg *Config) error {
	var problems []string
	if strings.TrimSpace(cfg.CoordinatorAddr) == "" {
		problems = append(problems, "coordinator_addr is required")
	}
	if cfg.State.SQLitePath == "" {
		problems = append(problems, "state.sqlite_path is required")
	}
	if cfg.Events.BatchMaxEvents <= 0 || cfg.Events.BatchMaxEvents > 5000 {
		problems = append(problems, "events.batch_max_events must be between 1 and 5000")
	}
	if cfg.Events.FlushInterval <= 0 {
		problems = append(problems, "events.flush_interval must be positive")
	}
	if cfg.Events.MaxOutboxBatches <= 0 {
		problems = append(problems, "events.max_outbox_batches must be positive")
	}
	if cfg.Events.RetryMin <= 0 || cfg.Events.RetryMax < cfg.Events.RetryMin {
		problems = append(problems, "events retry bounds are invalid")
	}
	if cfg.Events.SendTimeout <= 0 || cfg.Events.HeartbeatInterval <= 0 {
		problems = append(problems, "events.send_timeout and heartbeat_interval must be positive")
	}
	if cfg.Inspection.QueueMaxLen < 64 {
		problems = append(problems, "inspection.queue_max_len must be at least 64")
	}
	if cfg.Inspection.QueueMaxPacket < 512 {
		problems = append(problems, "inspection.queue_max_packet must be at least 512")
	}
	if cfg.Inspection.FlowDumpInterval < 200*time.Millisecond {
		problems = append(problems, "inspection.flow_snapshot_interval must be at least 200ms")
	}
	if cfg.Inspection.DNSQueueNum == cfg.Inspection.TLSQueueNum {
		problems = append(problems, "inspection.dns_queue_num and tls_queue_num must differ")
	}
	for _, port := range cfg.Inspection.TLSPorts {
		if port == 0 || port > 65535 {
			problems = append(problems, "inspection.tls_ports contains an invalid port")
			break
		}
	}
	if cfg.Firewall.Enabled && strings.TrimSpace(cfg.Firewall.TableName) == "" {
		problems = append(problems, "firewall.table_name is required")
	}
	if cfg.Docker.Enabled && cfg.Docker.Socket == "" {
		problems = append(problems, "docker.socket is required when docker is enabled")
	}
	if cfg.TLS.Enabled {
		for name, path := range map[string]string{
			"tls.ca_file":   cfg.TLS.CAFile,
			"tls.cert_file": cfg.TLS.CertFile,
			"tls.key_file":  cfg.TLS.KeyFile,
		} {
			if path == "" {
				problems = append(problems, name+" is required when TLS is enabled")
				continue
			}
			if _, err := os.Stat(path); err != nil {
				problems = append(problems, fmt.Sprintf("%s is not readable: %v", name, err))
			}
		}
		if cfg.TLS.ServerName == "" {
			problems = append(problems, "tls.server_name is required")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}
