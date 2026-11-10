package asn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestLookup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/prefixes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") != "AS15169" {
			t.Errorf("unexpected resource %q", r.URL.Query().Get("resource"))
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"prefixes":[
			{"prefix":"8.8.4.0/24"},{"prefix":"2001:4860::/32"},{"prefix":"8.8.4.0/24"},{"prefix":"bad"}]}}`))
	})
	mux.HandleFunc("/holder", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","data":{"holder":"Google LLC"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resolver := New(srv.URL+"/prefixes?resource=AS{asn}", srv.URL+"/holder?resource=AS{asn}", 2*time.Second, 100, zerolog.Nop())
	result, err := resolver.Lookup(context.Background(), 15169)
	if err != nil {
		t.Fatal(err)
	}
	if result.Holder != "Google LLC" {
		t.Fatalf("holder = %q", result.Holder)
	}
	want := []string{"8.8.4.0/24", "2001:4860::/32"}
	if strings.Join(result.Prefixes, ",") != strings.Join(want, ",") {
		t.Fatalf("prefixes = %v", result.Prefixes)
	}
}

func TestLookupErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/none", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","data":{"prefixes":[]}}`))
	})
	mux.HandleFunc("/many", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","data":{"prefixes":[{"prefix":"10.0.0.0/8"},{"prefix":"11.0.0.0/8"}]}}`))
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/none", "/many", "/fail"} {
		resolver := New(srv.URL+path+"?resource=AS{asn}", "", time.Second, 1, zerolog.Nop())
		if _, err := resolver.Lookup(context.Background(), 1); err == nil {
			t.Fatalf("%s: expected an error", path)
		}
	}
}

func TestValidateTemplates(t *testing.T) {
	if err := ValidateTemplates("https://example/{asn}", "https://example/{asn}"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTemplates("https://example", ""); err == nil {
		t.Fatal("expected missing placeholder error")
	}
}
