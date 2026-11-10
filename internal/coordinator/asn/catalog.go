package asn

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

const maxCatalogBytes = 128 << 20

// CatalogEntry is one ASN in the searchable directory.
type CatalogEntry struct {
	ASN     int64
	Name    string
	Country string
}

// Catalog downloads and parses an ASN directory. The default URL is iptoasn's
// combined table, which carries the holder name and country for every routed
// prefix; entries are deduplicated by ASN.
type Catalog struct {
	client *http.Client
	url    string
	logger zerolog.Logger
}

// NewCatalog builds a catalog loader.
func NewCatalog(url string, timeout time.Duration, logger zerolog.Logger) *Catalog {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Catalog{client: &http.Client{Timeout: timeout}, url: url, logger: logger}
}

// Load downloads the directory and returns unique ASNs.
func (c *Catalog) Load(ctx context.Context) ([]CatalogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("build ASN catalog request: %w", err)
	}
	req.Header.Set("User-Agent", "Kurwall/0.1 ASN catalog")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download ASN catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ASN catalog returned HTTP %d", resp.StatusCode)
	}
	var reader io.Reader = io.LimitReader(resp.Body, maxCatalogBytes)
	buffered := bufio.NewReader(reader)
	if magic, _ := buffered.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, fmt.Errorf("open ASN catalog gzip: %w", err)
		}
		defer gz.Close()
		reader = gz
	} else {
		reader = buffered
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := map[int64]CatalogEntry{}
	lines := 0
	for scanner.Scan() {
		lines++
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 5 {
			continue
		}
		asn, err := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 32)
		if err != nil || asn == 0 {
			continue
		}
		country := strings.ToUpper(strings.TrimSpace(fields[3]))
		if len(country) != 2 || country == "NONE" || !isCountry(country) {
			continue
		}
		name := strings.TrimSpace(fields[4])
		if name == "" || name == "-" || strings.EqualFold(name, "none") || strings.EqualFold(name, "not routed") {
			continue
		}
		if len(name) > 300 {
			name = name[:300]
		}
		seen[int64(asn)] = CatalogEntry{ASN: int64(asn), Name: name, Country: country}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ASN catalog: %w", err)
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("ASN catalog contained no usable entries")
	}
	out := make([]CatalogEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	c.logger.Info().Int("lines", lines).Int("asns", len(out)).Msg("ASN catalog parsed")
	return out, nil
}
func isCountry(value string) bool {
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
