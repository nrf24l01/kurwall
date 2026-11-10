package asn

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestCatalogLoad(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(
		"1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
			"1.0.1.0\t1.0.3.255\t0\tNone\tNot routed\n" +
			"1.0.4.0\t1.0.7.255\t42\tNO\tWOODYNET\n" +
			"2.0.0.0\t2.0.0.255\t11157\tZA\t-\n" +
			"3.0.0.0\t3.0.0.255\t13335\tUS\tCLOUDFLARENET\n"))
	_ = gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	catalog := NewCatalog(srv.URL, 2*time.Second, zerolog.Nop())
	entries, err := catalog.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	seen := map[int64]CatalogEntry{}
	for _, entry := range entries {
		seen[entry.ASN] = entry
	}
	if seen[13335].Country != "US" || seen[13335].Name != "CLOUDFLARENET" {
		t.Fatalf("unexpected 13335: %+v", seen[13335])
	}
	if seen[42].Country != "NO" {
		t.Fatalf("Norway was dropped: %+v", seen[42])
	}
}
