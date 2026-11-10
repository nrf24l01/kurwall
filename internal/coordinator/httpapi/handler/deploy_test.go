package handler

import "testing"

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
