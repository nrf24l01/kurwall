// Package config loads the agent YAML configuration with environment
// overrides and validates it before any privileged operation starts.
package config

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the complete agent configuration.
type Config struct {
	NodeID          string           `yaml:"node_id"`
	CoordinatorAddr string           `yaml:"coordinator_addr"`
	TLS             TLSConfig        `yaml:"tls"`
	State           StateConfig      `yaml:"state"`
	Inspection      InspectionConfig `yaml:"inspection"`
	Firewall        FirewallConfig   `yaml:"firewall"`
	Docker          DockerConfig     `yaml:"docker"`
	Events          EventsConfig     `yaml:"events"`
	Metrics         MetricsConfig    `yaml:"metrics"`
	Log             LogConfig        `yaml:"log"`
}

type TLSConfig struct {
	Enabled    bool   `yaml:"enabled"`
	CAFile     string `yaml:"ca_file"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	ServerName string `yaml:"server_name"`
}

type StateConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

type InspectionConfig struct {
	DNSEnabled         bool          `yaml:"dns_enabled"`
	TLSEnabled         bool          `yaml:"tls_enabled"`
	TLSPorts           []uint32      `yaml:"tls_ports"`
	DNSQueueNum        uint16        `yaml:"dns_queue_num"`
	TLSQueueNum        uint16        `yaml:"tls_queue_num"`
	QueueMaxLen        uint32        `yaml:"queue_max_len"`
	QueueMaxPacket     uint32        `yaml:"queue_max_packet"`
	DNSResponseTimeout time.Duration `yaml:"dns_response_timeout"`
	BlockedConnTTL     time.Duration `yaml:"blocked_conn_ttl"`
	FlowDumpInterval   time.Duration `yaml:"flow_snapshot_interval"`
}

type FirewallConfig struct {
	Enabled       bool   `yaml:"enabled"`
	TableName     string `yaml:"table_name"`
	BlockLogGroup uint16 `yaml:"block_log_group"`
}

type DockerConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Socket          string        `yaml:"socket"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

type EventsConfig struct {
	BatchMaxEvents    int           `yaml:"batch_max_events"`
	FlushInterval     time.Duration `yaml:"flush_interval"`
	MaxOutboxBatches  int           `yaml:"max_outbox_batches"`
	RetryMin          time.Duration `yaml:"retry_min"`
	RetryMax          time.Duration `yaml:"retry_max"`
	SendTimeout       time.Duration `yaml:"send_timeout"`
	MaxAttempts       int           `yaml:"max_attempts"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	ReconnectMax      time.Duration `yaml:"reconnect_max"`
}

type MetricsConfig struct {
	Addr string `yaml:"addr"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns development-friendly defaults.
func Default() Config {
	return Config{
		CoordinatorAddr: "127.0.0.1:9090",
		TLS: TLSConfig{
			Enabled:    true,
			CAFile:     "/etc/kurwall/tls/ca.crt",
			CertFile:   "/etc/kurwall/tls/agent.crt",
			KeyFile:    "/etc/kurwall/tls/agent.key",
			ServerName: "kurwall-coordinator",
		},
		State: StateConfig{SQLitePath: "/var/lib/kurwall/agent.db"},
		Inspection: InspectionConfig{
			DNSEnabled:         true,
			TLSEnabled:         true,
			TLSPorts:           []uint32{443},
			DNSQueueNum:        0,
			TLSQueueNum:        1,
			QueueMaxLen:        2048,
			QueueMaxPacket:     4096,
			DNSResponseTimeout: 5 * time.Second,
			BlockedConnTTL:     5 * time.Minute,
			FlowDumpInterval:   time.Second,
		},
		Firewall: FirewallConfig{Enabled: true, TableName: "kurwall", BlockLogGroup: 5},
		Docker:   DockerConfig{Enabled: true, Socket: "/var/run/docker.sock", RefreshInterval: 15 * time.Second},
		Events: EventsConfig{
			BatchMaxEvents:    500,
			FlushInterval:     time.Second,
			MaxOutboxBatches:  100000,
			RetryMin:          time.Second,
			RetryMax:          60 * time.Second,
			SendTimeout:       30 * time.Second,
			MaxAttempts:       10,
			HeartbeatInterval: 15 * time.Second,
			ReconnectMax:      60 * time.Second,
		},
		Metrics: MetricsConfig{Addr: "127.0.0.1:9877"},
		Log:     LogConfig{Level: "info", Format: "json"},
	}
}

// Load reads YAML, applies environment overrides, resolves the node identity
// and validates the result.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, fmt.Errorf("config path is required (APP_CONFIG_PATH)")
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config %s: %w", path, err)
	}
	applyEnv(&cfg)
	if err := Validate(&cfg); err != nil {
		return cfg, err
	}
	if cfg.NodeID == "" {
		cfg.NodeID = resolveNodeID(cfg)
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	setString(&cfg.NodeID, "KURWALL_NODE_ID")
	setString(&cfg.CoordinatorAddr, "KURWALL_COORDINATOR_ADDR")
	setString(&cfg.State.SQLitePath, "KURWALL_SQLITE_PATH")
	setString(&cfg.TLS.CAFile, "KURWALL_TLS_CA_FILE")
	setString(&cfg.TLS.CertFile, "KURWALL_TLS_CERT_FILE")
	setString(&cfg.TLS.KeyFile, "KURWALL_TLS_KEY_FILE")
	setString(&cfg.TLS.ServerName, "KURWALL_TLS_SERVER_NAME")
	setString(&cfg.Metrics.Addr, "KURWALL_METRICS_ADDR")
	setString(&cfg.Log.Level, "KURWALL_LOG_LEVEL")
	setString(&cfg.Log.Format, "KURWALL_LOG_FORMAT")
}

func setString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		*dst = v
	}
}

func resolveNodeID(cfg Config) string {
	if cfg.TLS.CertFile != "" {
		if cn := certificateCN(cfg.TLS.CertFile); cn != "" {
			return cn
		}
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "kurwall-agent"
	}
	return host
}

func certificateCN(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cert.Subject.CommonName)
}
