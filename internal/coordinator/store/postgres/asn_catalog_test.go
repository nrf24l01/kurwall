package postgres

import (
	"net/netip"
	"testing"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

func TestASNRangeSourceStreamsRows(t *testing.T) {
	ranges := []domain.ASNRange{
		{
			StartIP: netip.MustParseAddr("1.0.0.0"), EndIP: netip.MustParseAddr("1.0.0.255"),
			ASN: 13335, Country: "US", Name: "CLOUDFLARENET",
		},
		{
			StartIP: netip.MustParseAddr("2001:4860::"), EndIP: netip.MustParseAddr("2001:4860::ffff"),
			ASN: 42, Country: "NO", Name: "WOODYNET",
		},
	}
	src := &asnRangeSource{ranges: ranges}
	seen := 0
	for src.Next() {
		row, err := src.Values()
		if err != nil {
			t.Fatal(err)
		}
		want := ranges[seen]
		if row[0] != want.StartIP || row[1] != want.EndIP || row[2] != want.ASN ||
			row[3] != want.Country || row[4] != want.Name {
			t.Fatalf("row %d = %+v, want %+v", seen, row, want)
		}
		seen++
	}
	if err := src.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(ranges) {
		t.Fatalf("rows = %d, want %d", seen, len(ranges))
	}
}

func TestASNRangeSourceRejectsInvalidAddress(t *testing.T) {
	src := &asnRangeSource{ranges: []domain.ASNRange{{ASN: 64500, Country: "US", Name: "BROKEN"}}}
	if !src.Next() {
		t.Fatal("Next() = false, want true")
	}
	if _, err := src.Values(); err == nil {
		t.Fatal("Values() error = nil, want invalid address error")
	}
	if src.Next() {
		t.Fatal("Next() after error = true, want false")
	}
	if src.Err() == nil {
		t.Fatal("Err() = nil, want error")
	}
}
