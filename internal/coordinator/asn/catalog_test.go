package asn

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
			"3.0.0.0\t3.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
			"2001:4860::\t2001:4860:ffff:ffff:ffff:ffff:ffff:ffff\t13335\tUS\tCLOUDFLARENET\n"))
	_ = gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	catalog := NewCatalog(srv.URL, 2*time.Second, zerolog.Nop())
	result, err := catalog.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(result.Entries), result.Entries)
	}
	seen := map[int64]CatalogEntry{}
	for _, entry := range result.Entries {
		seen[entry.ASN] = entry
	}
	if seen[13335].Country != "US" || seen[13335].Name != "CLOUDFLARENET" {
		t.Fatalf("unexpected 13335: %+v", seen[13335])
	}
	if seen[42].Country != "NO" || seen[42].Name != "WOODYNET" {
		t.Fatalf("Norway was dropped: %+v", seen[42])
	}

	if len(result.Ranges) != 4 {
		t.Fatalf("ranges = %d, want 4: %+v", len(result.Ranges), result.Ranges)
	}
	ranges := map[netip.Addr]CatalogRange{}
	for _, r := range result.Ranges {
		ranges[r.Start] = r
	}
	cases := []struct {
		start, end string
		asn        int64
		country    string
	}{
		{"1.0.0.0", "1.0.0.255", 13335, "US"},
		{"1.0.4.0", "1.0.7.255", 42, "NO"},
		{"3.0.0.0", "3.0.0.255", 13335, "US"},
		{"2001:4860::", "2001:4860:ffff:ffff:ffff:ffff:ffff:ffff", 13335, "US"},
	}
	for _, tc := range cases {
		start := netip.MustParseAddr(tc.start)
		got, ok := ranges[start]
		if !ok {
			t.Fatalf("missing range starting at %s", tc.start)
		}
		if got.End != netip.MustParseAddr(tc.end) || got.ASN != tc.asn || got.Country != tc.country {
			t.Fatalf("range %s = %+v, want end %s asn %d country %s", tc.start, got, tc.end, tc.asn, tc.country)
		}
	}
}
