package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Validate rejects configuration that cannot work safely in production.
func Validate(cfg *Config) error {
	var problems []string
	if cfg.Server.HTTPAddr == "" {
		problems = append(problems, "server.http_addr is required")
	}
	if cfg.Server.GRPCAddr == "" {
		problems = append(problems, "server.grpc_addr is required")
	}
	if cfg.Server.AgentDistDir == "" {
		problems = append(problems, "server.agent_dist_dir is required")
	}
	if cfg.Server.EnrollmentDir == "" {
		problems = append(problems, "server.enrollment_dir is required")
	}
	if cfg.Server.PublicURL != "" {
		if err := validatePublicURL(cfg.Server.PublicURL); err != nil {
			problems = append(problems, "server.public_url: "+err.Error())
		}
	}
	if cfg.Server.GRPCPublicAddr != "" {
		if err := validatePublicAddr(cfg.Server.GRPCPublicAddr); err != nil {
			problems = append(problems, "server.grpc_public_addr: "+err.Error())
		}
	}
	if err := validateServerName(cfg.Server.GRPCServerName); err != nil {
		problems = append(problems, "server.grpc_server_name: "+err.Error())
	}
	if cfg.Postgres.Host == "" || cfg.Postgres.Database == "" || cfg.Postgres.User == "" {
		problems = append(problems, "postgres.host, postgres.database and postgres.user are required")
	}
	if cfg.Postgres.Port <= 0 || cfg.Postgres.Port > 65535 {
		problems = append(problems, "postgres.port must be between 1 and 65535")
	}
	if cfg.Postgres.MaxConns <= 0 {
		problems = append(problems, "postgres.max_conns must be positive")
	}
	if cfg.ClickHouse.Addr == "" || cfg.ClickHouse.Database == "" {
		problems = append(problems, "clickhouse.addr and clickhouse.database are required")
	}
	if cfg.ClickHouse.InsertBatchSize <= 0 {
		problems = append(problems, "clickhouse.insert_batch_size must be positive")
	}
	if cfg.Ingest.MaxBatchEvents <= 0 {
		problems = append(problems, "ingest.max_batch_events must be positive")
	}
	if cfg.Agent.BatchMaxEvents == 0 {
		problems = append(problems, "agent.batch_max_events must be positive")
	}
	if cfg.Agent.FlushInterval <= 0 {
		problems = append(problems, "agent.flush_interval must be positive")
	}
	if cfg.Agent.OfflineAfter <= 0 {
		problems = append(problems, "agent.offline_after must be positive")
	}
	for _, port := range cfg.Agent.TLSInspectorPorts {
		if port == 0 || port > 65535 {
			problems = append(problems, "agent.tls_inspector_ports contains an invalid port")
			break
		}
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
	}
	if cfg.TLS.RequireClientCert && !cfg.TLS.Enabled {
		problems = append(problems, "tls.require_client_cert requires tls.enabled")
	}
	if cfg.ASN.CatalogEnabled {
		if cfg.ASN.CatalogURL == "" {
			problems = append(problems, "asn.catalog_url is required when the ASN catalog is enabled")
		}
		if cfg.ASN.CatalogRefresh <= 0 || cfg.ASN.CatalogRetry <= 0 || cfg.ASN.CatalogTimeout <= 0 {
			problems = append(problems, "asn catalog intervals must be positive")
		}
	}
	if cfg.ASN.Enabled {
		if cfg.ASN.PrefixURLTemplate == "" {
			problems = append(problems, "asn.prefix_url_template is required when ASN resolution is enabled")
		}
		if !strings.Contains(cfg.ASN.PrefixURLTemplate, "{asn}") {
			problems = append(problems, "asn.prefix_url_template must contain {asn}")
		}
		if cfg.ASN.HolderURLTemplate != "" && !strings.Contains(cfg.ASN.HolderURLTemplate, "{asn}") {
			problems = append(problems, "asn.holder_url_template must contain {asn}")
		}
		if cfg.ASN.RefreshInterval <= 0 || cfg.ASN.RetryInterval <= 0 {
			problems = append(problems, "asn refresh and retry intervals must be positive")
		}
		if cfg.ASN.RequestTimeout <= 0 {
			problems = append(problems, "asn.request_timeout must be positive")
		}
		if cfg.ASN.MaxPrefixes <= 0 {
			problems = append(problems, "asn.max_prefixes must be positive")
		}
	}
	if cfg.Auth.APIToken != "" && len(cfg.Auth.APIToken) < 16 {
		problems = append(problems, "auth.api_token must be at least 16 characters")
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// validatePublicURL accepts only absolute HTTP(S) URLs without characters that
// would need special handling when the URL is embedded in a shell script.
func validatePublicURL(raw string) error {
	if strings.ContainsAny(raw, "'\"`$\\ \n\r\t") {
		return fmt.Errorf("must not contain quotes, backticks, dollar signs, backslashes or whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("must include a host")
	}
	if parsed.User != nil {
		return fmt.Errorf("must not include credentials")
	}
	return nil
}

// validateServerName accepts the TLS server name agents use for the gRPC
// connection. It must be a bare hostname or IP address, never a URL or host:port.
func validateServerName(raw string) error {
	if raw == "" {
		return fmt.Errorf("is required")
	}
	if strings.ContainsAny(raw, "'\"`$\\/ \n\r\t") {
		return fmt.Errorf("must not contain quotes, backticks, dollar signs, backslashes, slashes or whitespace")
	}
	if net.ParseIP(raw) != nil {
		return nil
	}
	if strings.Contains(raw, ":") {
		return fmt.Errorf("must be a hostname without a port")
	}
	if len(raw) > 253 {
		return fmt.Errorf("is too long")
	}
	for _, label := range strings.Split(raw, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("is not a valid hostname")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("is not a valid hostname")
			}
		}
	}
	return nil
}

// validatePublicAddr accepts a host:port pair suitable for the agent gRPC
// connection.
func validatePublicAddr(raw string) error {
	if strings.ContainsAny(raw, "'\"`$\\ \n\r\t") {
		return fmt.Errorf("must not contain quotes, backticks, dollar signs, backslashes or whitespace")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host == "" {
		return fmt.Errorf("must be a host:port pair")
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("has an invalid port")
	}
	return nil
}
