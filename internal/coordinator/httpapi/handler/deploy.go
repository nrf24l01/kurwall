package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

//go:embed install.sh.tmpl
var installScriptSource string

var installScript = template.Must(template.New("install").Parse(installScriptSource))

type installData struct {
	BaseURL   string
	GRPCAddr  string
	DeployKey string
}

// deployBaseURL returns the public HTTP origin nodes use for installation and
// updates. server.public_url wins; otherwise the request origin is used.
func (h *Handler) deployBaseURL(c echo.Context) (string, error) {
	raw := h.PublicURL
	if raw == "" {
		scheme := "http"
		if c.Request().TLS != nil {
			scheme = "https"
		}
		if proto := firstForwarded(c, "X-Forwarded-Proto"); proto != "" {
			scheme = proto
		}
		host := c.Request().Host
		if forwarded := firstForwarded(c, "X-Forwarded-Host"); forwarded != "" {
			host = forwarded
		}
		raw = scheme + "://" + host
	}
	raw = strings.TrimRight(raw, "/")
	if !safeDeployURL(raw) {
		return "", errors.New("server.public_url is not a safe absolute HTTP(S) URL")
	}
	return raw, nil
}

// deployGRPCAddr returns the address agents dial. It falls back to the public
// host and the configured gRPC port.
func (h *Handler) deployGRPCAddr(c echo.Context) (string, error) {
	if h.GRPCPublicAddr != "" {
		return h.GRPCPublicAddr, nil
	}
	port := "9090"
	if _, configured, err := net.SplitHostPort(h.GRPCAddr); err == nil && configured != "" {
		port = configured
	}
	host := ""
	if h.PublicURL != "" {
		if parsed, err := url.Parse(h.PublicURL); err == nil {
			host = parsed.Hostname()
		}
	}
	if host == "" {
		host = c.Request().Host
		if withoutPort, _, err := net.SplitHostPort(host); err == nil {
			host = withoutPort
		}
	}
	if host == "" {
		return "", errors.New("cannot determine the public coordinator address; set server.grpc_public_addr")
	}
	return net.JoinHostPort(host, port), nil
}

func firstForwarded(c echo.Context, header string) string {
	value := c.Request().Header.Get(header)
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(value, ",")[0])
}

func safeDeployURL(raw string) bool {
	if strings.ContainsAny(raw, "'\"`$\\ \n\r\t") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// agentDistribution reports the version and availability of the published
// agent assets.
func (h *Handler) agentDistribution() (string, bool) {
	if h.AgentDistDir == "" {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(h.AgentDistDir, "version.txt"))
	if err != nil {
		return "", false
	}
	version := strings.TrimSpace(string(raw))
	return version, version != ""
}

// InstallScript renders the install/update script with the public origin.
func (h *Handler) InstallScript(c echo.Context) error {
	base, err := h.deployBaseURL(c)
	if err != nil {
		return response.Error(c, apperr.Internal("build installer URL", err))
	}
	grpcAddr, err := h.deployGRPCAddr(c)
	if err != nil {
		return response.Error(c, apperr.Validation(err.Error()))
	}
	deployKey := c.QueryParam("key")
	if deployKey == "" {
		deployKey = c.QueryParam("deploy_key")
	}
	if deployKey != "" && !auth.ValidTokenFormat(deployKey) {
		return response.Error(c, apperr.Validation("invalid deploy key in the install URL"))
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/x-shellscript; charset=utf-8")
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Content-Disposition", `inline; filename="install.sh"`)
	return installScript.Execute(c.Response(), installData{BaseURL: base, GRPCAddr: grpcAddr, DeployKey: deployKey})
}

// AgentAsset serves the public installer assets (binaries, unit files, CA and
// checksums) from the configured distribution directory.
func (h *Handler) AgentAsset(c echo.Context) error {
	raw := c.Param("*")
	name := path.Base(raw)
	if raw == "" || name != raw || name == "." || name == ".." {
		return echo.NotFoundHandler(c)
	}
	if !allowedAgentAsset(name) {
		return echo.NotFoundHandler(c)
	}
	full := filepath.Join(h.AgentDistDir, name)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return echo.NotFoundHandler(c)
	}

	switch {
	case strings.HasSuffix(name, ".json"):
		c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	case strings.HasSuffix(name, ".crt"):
		c.Response().Header().Set(echo.HeaderContentType, "application/x-pem-file")
	case strings.HasSuffix(name, ".txt"), strings.HasSuffix(name, ".conf"), strings.HasSuffix(name, ".service"):
		c.Response().Header().Set(echo.HeaderContentType, "text/plain; charset=utf-8")
	default:
		c.Response().Header().Set(echo.HeaderContentType, "application/octet-stream")
	}
	c.Response().Header().Set("Cache-Control", "public, max-age=60")
	return c.File(full)
}

func allowedAgentAsset(name string) bool {
	switch name {
	case "version.txt", "checksums.txt", "manifest.json", "ca.crt", "kurwall-agent.service", "99-kurwall.conf":
		return true
	}
	return strings.HasPrefix(name, "kurwall-agent-linux-")
}

type createDeployKeyRequest struct {
	NodeName   string   `json:"nodeName"`
	TTLMinutes int      `json:"ttlMinutes"`
	PolicyIDs  []string `json:"policyIds"`
}

// CreateDeployKey returns a one-time install link bound to a node name.
func (h *Handler) CreateDeployKey(c echo.Context) error {
	if h.Deploy == nil {
		return response.Error(c, apperr.Unavailable("ENROLLMENT_DISABLED", "node enrollment is disabled", nil))
	}
	var body createDeployKeyRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	identity, _ := auth.IdentityFrom(c.Request().Context())
	ttl := time.Duration(body.TTLMinutes) * time.Minute
	key, token, err := h.Deploy.Create(c.Request().Context(), body.NodeName, body.PolicyIDs, ttl, identity.Username)
	if err != nil {
		return response.Error(c, err)
	}
	base, err := h.deployBaseURL(c)
	if err != nil {
		return response.Error(c, apperr.Internal("build install URL", err))
	}
	installURL := base + "/install.sh?key=" + url.QueryEscape(token)
	return response.Created(c, map[string]any{
		"key":            key,
		"token":          token,
		"installUrl":     installURL,
		"installCommand": "curl -fsSL '" + installURL + "' | sudo bash",
	})
}

// ListDeployKeys returns recent deploy keys without plaintext tokens.
func (h *Handler) ListDeployKeys(c echo.Context) error {
	if h.Deploy == nil {
		return response.OK(c, map[string]any{"items": []any{}})
	}
	keys, err := h.Deploy.List(c.Request().Context())
	if err != nil {
		return response.Error(c, err)
	}
	if keys == nil {
		keys = []domain.DeployKey{}
	}
	return response.OK(c, map[string]any{"items": keys})
}

// DeleteDeployKey revokes an unused key.
func (h *Handler) DeleteDeployKey(c echo.Context) error {
	if h.Deploy == nil {
		return response.Error(c, apperr.NotFound("DEPLOY_KEY_NOT_FOUND", "deploy key not found"))
	}
	if err := h.Deploy.Revoke(c.Request().Context(), c.Param("id")); err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, map[string]any{"deleted": true})
}

// Enroll accepts a CSR protected by a deploy key and returns a signed node
// certificate and the CA in a gzip tarball.
func (h *Handler) Enroll(c echo.Context) error {
	if h.Deploy == nil {
		return response.Error(c, apperr.Unavailable("ENROLLMENT_DISABLED", "node enrollment is disabled", nil))
	}
	token := c.Request().Header.Get("X-Deploy-Key")
	if token == "" {
		token = c.QueryParam("key")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 64<<10))
	if err != nil {
		return response.Error(c, apperr.InvalidRequest("could not read certificate request"))
	}
	cert, ca, nodeName, err := h.Deploy.Enroll(c.Request().Context(), token, body, c.RealIP())
	if err != nil {
		return response.Error(c, err)
	}

	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := addArchiveFile(tarWriter, "agent.crt", cert); err != nil {
		return response.Error(c, apperr.Internal("package certificate", err))
	}
	if err := addArchiveFile(tarWriter, "ca.crt", ca); err != nil {
		return response.Error(c, apperr.Internal("package CA", err))
	}
	if err := tarWriter.Close(); err != nil {
		return response.Error(c, apperr.Internal("package enrollment", err))
	}
	if err := gzipWriter.Close(); err != nil {
		return response.Error(c, apperr.Internal("compress enrollment", err))
	}

	c.Response().Header().Set(echo.HeaderContentType, "application/gzip")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="kurwall-enrollment.tar.gz"`)
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("X-Kurwall-Node", nodeName)
	c.Response().WriteHeader(http.StatusOK)
	_, err = c.Response().Write(archive.Bytes())
	return err
}

func addArchiveFile(writer *tar.Writer, name string, data []byte) error {
	header := &tar.Header{
		Name: name, Mode: 0644, Size: int64(len(data)),
		ModTime: time.Now().UTC(),
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}
