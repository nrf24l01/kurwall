package config

import "testing"

func TestValidatePublicURL(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "http", value: "http://192.0.2.10:8080"},
		{name: "https with path", value: "https://kurwall.example.com/kurwall"},
		{name: "empty is not a URL", value: "", wantErr: true},
		{name: "missing scheme", value: "kurwall.example.com", wantErr: true},
		{name: "unsupported scheme", value: "ftp://kurwall.example.com", wantErr: true},
		{name: "missing host", value: "https://", wantErr: true},
		{name: "credentials", value: "https://user:pass@kurwall.example.com", wantErr: true},
		{name: "single quote", value: "https://kurwall.example.com/'x", wantErr: true},
		{name: "backtick", value: "https://kurwall.example.com/`id`", wantErr: true},
		{name: "dollar", value: "https://kurwall.example.com/$HOME", wantErr: true},
		{name: "space", value: "https://kurwall.example.com/a b", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePublicURL(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePublicURL(%q) error = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestValidatePublicAddr(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "ipv4", value: "192.0.2.10:9090"},
		{name: "hostname", value: "kurwall.example.com:9090"},
		{name: "ipv6", value: "[2001:db8::1]:9090"},
		{name: "missing port", value: "kurwall.example.com", wantErr: true},
		{name: "missing host", value: ":9090", wantErr: true},
		{name: "bad port", value: "kurwall.example.com:0", wantErr: true},
		{name: "quote", value: "kurwall.example.com:9090'", wantErr: true},
		{name: "space", value: "kurwall.example.com: 9090", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePublicAddr(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePublicAddr(%q) error = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestValidateServerName(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "hostname", value: "grpc.example.com"},
		{name: "short name", value: "kurwall-coordinator"},
		{name: "ipv4", value: "192.0.2.10"},
		{name: "ipv6", value: "2001:db8::1"},
		{name: "empty", value: "", wantErr: true},
		{name: "with port", value: "grpc.example.com:9090", wantErr: true},
		{name: "url", value: "https://grpc.example.com", wantErr: true},
		{name: "space", value: "grpc example.com", wantErr: true},
		{name: "quote", value: "grpc.example.com'", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateServerName(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateServerName(%q) error = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
		})
	}
}
