package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestAllowedAgentAsset(t *testing.T) {
	allowed := []string{
		"version.txt", "checksums.txt", "manifest.json", "ca.crt",
		"kurwall-agent.service", "99-kurwall.conf", "kurwall-agent-linux-amd64",
	}
	for _, name := range allowed {
		if !allowedAgentAsset(name) {
			t.Fatalf("expected %q to be allowed", name)
		}
	}
	rejected := []string{"coordinator.yaml", "agent.key", "agent.crt", "install.sh", "kurwall-agent", ""}
	for _, name := range rejected {
		if allowedAgentAsset(name) {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestSafeDeployURL(t *testing.T) {
	valid := []string{"http://192.0.2.10:8080", "https://kurwall.example.com/kurwall"}
	for _, value := range valid {
		if !safeDeployURL(value) {
			t.Fatalf("expected %q to be safe", value)
		}
	}
	invalid := []string{"", "kurwall.example.com", "ftp://kurwall.example.com", "http://x/'y", "http://x/`id`", "http://x/$HOME"}
	for _, value := range invalid {
		if safeDeployURL(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func installScriptBody(t *testing.T, h *Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if err := h.InstallScript(c); err != nil {
		t.Fatalf("InstallScript: %v", err)
	}
	return rec.Body.String()
}

func TestInstallScriptUsesSeparateWebAndGRPCHosts(t *testing.T) {
	body := installScriptBody(t, &Handler{
		PublicURL:      "https://panel.example.com",
		GRPCPublicAddr: "grpc.example.com:9090",
		GRPCServerName: "grpc.example.com",
	})
	for _, want := range []string{
		"BASE_URL='https://panel.example.com'",
		"COORDINATOR_ADDR='grpc.example.com:9090'",
		`server_name: "grpc.example.com"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("install script missing %q", want)
		}
	}
}

func TestInstallScriptDefaultsGRPCServerName(t *testing.T) {
	body := installScriptBody(t, &Handler{
		PublicURL:      "https://panel.example.com",
		GRPCPublicAddr: "grpc.example.com:9090",
	})
	if !strings.Contains(body, `server_name: "kurwall-coordinator"`) {
		t.Fatal("install script did not default the gRPC server name")
	}
}
