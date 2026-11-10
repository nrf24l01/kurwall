package tlsinspect

import (
	"testing"
)

// buildClientHello creates a minimal ClientHello with an SNI extension.
func buildClientHello(sni string) []byte {
	if sni == "" {
		// No extensions section.
		body := []byte{0x03, 0x03}
		body = append(body, make([]byte, 32)...)
		body = append(body, 0x00)                   // session id length
		body = append(body, 0x00, 0x02, 0x13, 0x01) // cipher suites
		body = append(body, 0x01, 0x00)             // compression methods
		body = append(body, 0x00, 0x00)             // extensions length
		return wrapRecord(body)
	}
	name := []byte(sni)
	entry := []byte{0x00, byte(len(name) >> 8), byte(len(name))}
	entry = append(entry, name...)
	serverName := []byte{byte(len(entry) >> 8), byte(len(entry))}
	serverName = append(serverName, entry...)
	ext := []byte{0x00, 0x00, byte(len(serverName) >> 8), byte(len(serverName))}
	ext = append(ext, serverName...)

	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)
	body = append(body, 0x00, 0x02, 0x13, 0x01)
	body = append(body, 0x01, 0x00)
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)
	return wrapRecord(body)
}

func wrapRecord(body []byte) []byte {
	record := []byte{0x16, 0x03, 0x01, byte((len(body) + 4) >> 8), byte(len(body) + 4)}
	record = append(record, 0x01, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	record = append(record, body...)
	return record
}

func TestExtractSNI(t *testing.T) {
	for _, name := range []string{"example.com", "api.Example.COM.", "xn--mnchen-3ya.de"} {
		raw := buildClientHello(name)
		got, ok := ExtractSNI(raw)
		if !ok {
			t.Fatalf("%s: expected SNI", name)
		}
		want := normalizeName(name)
		if got != want {
			t.Fatalf("%s: got %q", name, got)
		}
	}
}

func TestNoSNI(t *testing.T) {
	if _, ok := ExtractSNI(buildClientHello("")); ok {
		t.Fatal("expected no SNI")
	}
	if _, ok := ExtractSNI([]byte("GET / HTTP/1.1\r\n")); ok {
		t.Fatal("expected non-TLS rejection")
	}
}

func TestTruncated(t *testing.T) {
	raw := buildClientHello("example.com")
	if _, ok := ExtractSNI(raw[:20]); ok {
		t.Fatal("expected truncated rejection")
	}
}
