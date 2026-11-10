// Package config loads and validates coordinator configuration. YAML is the
// primary source; environment variables override deployment-specific values.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the complete coordinator configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Postgres   PostgresConfig   `yaml:"postgres"`
	ClickHouse ClickHouseConfig `yaml:"clickhouse"`
	TLS        TLSConfig        `yaml:"tls"`
	ASN        ASNConfig        `yaml:"asn"`
	Agent      AgentConfig      `yaml:"agent"`
	Auth       AuthConfig       `yaml:"auth"`
	Ingest     IngestConfig     `yaml:"ingest"`
	Log        LogConfig        `yaml:"log"`
}

type ServerConfig struct {
	HTTPAddr       string        `yaml:"http_addr"`
	GRPCAddr       string        `yaml:"grpc_addr"`
	WebStaticDir   string        `yaml:"web_static_dir"`
	PublicURL      string        `yaml:"public_url"`
	GRPCPublicAddr string        `yaml:"grpc_public_addr"`
	AgentDistDir   string        `yaml:"agent_dist_dir"`
	EnrollmentDir  string        `yaml:"enrollment_dir"`
	ReadTimeout    time.Duration `yaml:"read_timeout"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
}

type PostgresConfig struct {
	Host             string        `yaml:"host"`
	Port             int           `yaml:"port"`
	User             string        `yaml:"user"`
	Password         string        `yaml:"password"`
	Database         string        `yaml:"database"`
	SSLMode          string        `yaml:"sslmode"`
	MaxConns         int32         `yaml:"max_conns"`
	ConnectTimeout   time.Duration `yaml:"connect_timeout"`
	StatementTimeout time.Duration `yaml:"statement_timeout"`
}

type ClickHouseConfig struct {
	Addr            string        `yaml:"addr"`
	Database        string        `yaml:"database"`
	Username        string        `yaml:"username"`
	Password        string        `yaml:"password"`
	DialTimeout     time.Duration `yaml:"dial_timeout"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	InsertBatchSize int           `yaml:"insert_batch_size"`
	InsertInterval  time.Duration `yaml:"insert_interval"`
	QueryTimeout    time.Duration `yaml:"query_timeout"`
}

type TLSConfig struct {
	Enabled           bool   `yaml:"enabled"`
	CAFile            string `yaml:"ca_file"`
	CertFile          string `yaml:"cert_file"`
	KeyFile           string `yaml:"key_file"`
	RequireClientCert bool   `yaml:"require_client_cert"`
}

type ASNConfig struct {
	Enabled           bool          `yaml:"enabled"`
	PrefixURLTemplate string        `yaml:"prefix_url_template"`
	HolderURLTemplate string        `yaml:"holder_url_template"`
	RefreshInterval   time.Duration `yaml:"refresh_interval"`
	RetryInterval     time.Duration `yaml:"retry_interval"`
	RequestTimeout    time.Duration `yaml:"request_timeout"`
	MaxPrefixes       int           `yaml:"max_prefixes"`
	CatalogEnabled    bool          `yaml:"catalog_enabled"`
	CatalogURL        string        `yaml:"catalog_url"`
	CatalogRefresh    time.Duration `yaml:"catalog_refresh_interval"`
	CatalogRetry      time.Duration `yaml:"catalog_retry_interval"`
	CatalogTimeout    time.Duration `yaml:"catalog_timeout"`
}

type AgentConfig struct {
	TLSInspectorPorts []uint32      `yaml:"tls_inspector_ports"`
	DNSInspection     bool          `yaml:"dns_inspection"`
	TLSInspection     bool          `yaml:"tls_inspection"`
	BatchMaxEvents    uint32        `yaml:"batch_max_events"`
	FlushInterval     time.Duration `yaml:"flush_interval"`
	OfflineAfter      time.Duration `yaml:"offline_after"`
}

type AuthConfig struct {
	APIToken string `yaml:"api_token"`
}

type IngestConfig struct {
	MaxBatchEvents int           `yaml:"max_batch_events"`
	DedupRetention time.Duration `yaml:"dedup_retention"`
	DedupTimeout   time.Duration `yaml:"dedup_timeout"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns a fully populated configuration with development defaults.
func Default() Config {
	return Config{
		Server: ServerConfig{
			HTTPAddr:      ":8080",
			GRPCAddr:      ":9090",
			WebStaticDir:  "./web/dist",
			AgentDistDir:  "./agent-dist",
			EnrollmentDir: "/var/lib/kurwall/enrollment",
			ReadTimeout:   30 * time.Second,
			IdleTimeout:   120 * time.Second,
		},
		Postgres: PostgresConfig{
			Host:             "127.0.0.1",
			Port:             5432,
			User:             "kurwall",
			Database:         "kurwall",
			SSLMode:          "disable",
			MaxConns:         10,
			ConnectTimeout:   5 * time.Second,
			StatementTimeout: 30 * time.Second,
		},
		ClickHouse: ClickHouseConfig{
			Addr:            "127.0.0.1:9000",
			Database:        "kurwall",
			Username:        "default",
			DialTimeout:     5 * time.Second,
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			InsertBatchSize: 2000,
			InsertInterval:  500 * time.Millisecond,
			QueryTimeout:    15 * time.Second,
		},
		ASN: ASNConfig{
			Enabled:           true,
			PrefixURLTemplate: "https://stat.ripe.net/data/announced-prefixes/data.json?resource=AS{asn}",
			HolderURLTemplate: "https://stat.ripe.net/data/as-overview/data.json?resource=AS{asn}",
			RefreshInterval:   12 * time.Hour,
			RetryInterval:     5 * time.Minute,
			RequestTimeout:    10 * time.Second,
			MaxPrefixes:       20000,
			CatalogEnabled:    true,
			CatalogURL:        "https://iptoasn.com/data/ip2asn-combined.tsv.gz",
			CatalogRefresh:    168 * time.Hour,
			CatalogRetry:      5 * time.Minute,
			CatalogTimeout:    90 * time.Second,
		},
		TLS: TLSConfig{
			Enabled:           true,
			CAFile:            "/etc/kurwall/tls/ca.crt",
			CertFile:          "/etc/kurwall/tls/coordinator.crt",
			KeyFile:           "/etc/kurwall/tls/coordinator.key",
			RequireClientCert: true,
		},
		Agent: AgentConfig{
			TLSInspectorPorts: []uint32{443},
			DNSInspection:     true,
			TLSInspection:     true,
			BatchMaxEvents:    500,
			FlushInterval:     1000 * time.Millisecond,
			OfflineAfter:      90 * time.Second,
		},
		Ingest: IngestConfig{
			MaxBatchEvents: 5000,
			DedupRetention: 168 * time.Hour,
			DedupTimeout:   2 * time.Minute,
		},
		Log: LogConfig{Level: "info", Format: "json"},
	}
}

// Load reads YAML from path, applies environment overrides and returns a
// validated configuration.
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
	return cfg, nil
}

func applyEnv(cfg *Config) {
	setString(&cfg.Server.HTTPAddr, "KURWALL_HTTP_ADDR")
	setString(&cfg.Server.GRPCAddr, "KURWALL_GRPC_ADDR")
	setString(&cfg.Server.WebStaticDir, "KURWALL_WEB_STATIC_DIR")
	setString(&cfg.Server.PublicURL, "KURWALL_PUBLIC_URL")
	setString(&cfg.Server.GRPCPublicAddr, "KURWALL_GRPC_PUBLIC_ADDR")
	setString(&cfg.Server.AgentDistDir, "KURWALL_AGENT_DIST_DIR")
	setString(&cfg.Server.EnrollmentDir, "KURWALL_ENROLLMENT_DIR")

	setString(&cfg.Postgres.Host, "POSTGRES_HOST")
	setInt(&cfg.Postgres.Port, "POSTGRES_PORT")
	setString(&cfg.Postgres.User, "POSTGRES_USER")
	setString(&cfg.Postgres.Password, "POSTGRES_PASSWORD")
	setString(&cfg.Postgres.Database, "POSTGRES_DB")
	setString(&cfg.Postgres.SSLMode, "POSTGRES_SSLMODE")

	setString(&cfg.ClickHouse.Addr, "CLICKHOUSE_ADDR")
	setString(&cfg.ClickHouse.Database, "CLICKHOUSE_DB")
	setString(&cfg.ClickHouse.Username, "CLICKHOUSE_USER")
	setString(&cfg.ClickHouse.Password, "CLICKHOUSE_PASSWORD")

	setString(&cfg.TLS.CAFile, "KURWALL_TLS_CA_FILE")
	setString(&cfg.TLS.CertFile, "KURWALL_TLS_CERT_FILE")
	setString(&cfg.TLS.KeyFile, "KURWALL_TLS_KEY_FILE")

	setString(&cfg.ASN.PrefixURLTemplate, "KURWALL_ASN_PREFIX_URL")
	setString(&cfg.ASN.HolderURLTemplate, "KURWALL_ASN_HOLDER_URL")
	setString(&cfg.ASN.CatalogURL, "KURWALL_ASN_CATALOG_URL")

	setString(&cfg.Auth.APIToken, "KURWALL_API_TOKEN")
	setString(&cfg.Log.Level, "KURWALL_LOG_LEVEL")
	setString(&cfg.Log.Format, "KURWALL_LOG_FORMAT")
}

func setString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		*dst = v
	}
}

func setInt(dst *int, key string) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return
	}
	var parsed int
	if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
		*dst = parsed
	}
}

// DSN builds the pgx connection string.
func (c PostgresConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=%d",
		c.Host, c.Port, c.User, c.Password, c.Database, c.SSLMode, int(c.ConnectTimeout.Seconds()))
}
