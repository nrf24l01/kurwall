package service

import (
	"context"
	"testing"
	"time"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

// stubEventReader records the queries the service forwards to ClickHouse and
// returns empty pages; the embedded nil interface covers unused methods.
type stubEventReader struct {
	domain.EventReader
	traffic domain.TrafficQuery
	dns     domain.DNSQuery
	flows   domain.FlowQuery
	blocks  domain.BlockQuery
}

func (s *stubEventReader) Flows(_ context.Context, q domain.FlowQuery) ([]domain.FlowRecord, uint64, error) {
	s.flows = q
	return nil, 0, nil
}

func (s *stubEventReader) DNS(_ context.Context, q domain.DNSQuery) ([]domain.DNSRecord, uint64, error) {
	s.dns = q
	return nil, 0, nil
}

func (s *stubEventReader) Blocks(_ context.Context, q domain.BlockQuery) ([]domain.BlockRecord, uint64, error) {
	s.blocks = q
	return nil, 0, nil
}

func (s *stubEventReader) Traffic(_ context.Context, q domain.TrafficQuery) ([]domain.TrafficEvent, uint64, error) {
	s.traffic = q
	return []domain.TrafficEvent{{DstIP: "203.0.113.10"}}, 1, nil
}

func (s *stubEventReader) Stats(context.Context, time.Time, time.Time) (domain.Stats, error) {
	return domain.Stats{}, nil
}

func (s *stubEventReader) Health(context.Context) error { return nil }

func TestNormalizeWindow(t *testing.T) {
	to := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	from := to.Add(-time.Hour)

	gotFrom, gotTo, err := normalizeWindow(time.Time{}, to, time.Hour)
	if err != nil {
		t.Fatalf("normalizeWindow default from: %v", err)
	}
	if !gotTo.Equal(to) || !gotFrom.Equal(from) {
		t.Fatalf("normalizeWindow = (%v, %v), want (%v, %v)", gotFrom, gotTo, from, to)
	}

	before := time.Now().UTC()
	gotFrom, gotTo, err = normalizeWindow(from, time.Time{}, time.Hour)
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("normalizeWindow default to: %v", err)
	}
	if !gotFrom.Equal(from) {
		t.Fatalf("from = %v, want %v", gotFrom, from)
	}
	if gotTo.Before(before) || gotTo.After(after) {
		t.Fatalf("default to = %v, want between %v and %v", gotTo, before, after)
	}

	// A non-UTC input keeps the same instant and is normalized to UTC.
	zone := time.FixedZone("CEST", 2*60*60)
	zoned := time.Date(2026, 11, 12, 14, 0, 0, 0, zone)
	gotFrom, gotTo, err = normalizeWindow(zoned.Add(-time.Hour), zoned, time.Hour)
	if err != nil {
		t.Fatalf("normalizeWindow zoned: %v", err)
	}
	if gotFrom.Location() != time.UTC || gotTo.Location() != time.UTC {
		t.Fatalf("normalizeWindow locations = (%v, %v), want UTC", gotFrom.Location(), gotTo.Location())
	}
	if !gotTo.Equal(zoned) || !gotFrom.Equal(zoned.Add(-time.Hour)) {
		t.Fatalf("zoned window = (%v, %v), want the same instants", gotFrom, gotTo)
	}

	if _, _, err := normalizeWindow(to, from, time.Hour); err == nil {
		t.Fatal("expected an error when from is after to")
	}
	if _, _, err := normalizeWindow(to.Add(-91*24*time.Hour), to, time.Hour); err == nil {
		t.Fatal("expected an error for a window longer than 90 days")
	}
	if _, _, err := normalizeWindow(to.Add(-90*24*time.Hour), to, time.Hour); err != nil {
		t.Fatalf("a 90-day window must be accepted: %v", err)
	}
	if _, _, err := normalizeWindow(to, time.Now().UTC().Add(25*time.Hour), time.Hour); err == nil {
		t.Fatal("expected an error for a to value far in the future")
	}
}

func TestTrafficNormalizesWindowBeforeQuery(t *testing.T) {
	to := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	from := to.Add(-30 * time.Minute)
	reader := &stubEventReader{}
	svc := &Events{reader: reader}

	page, err := svc.Traffic(context.Background(), domain.TrafficQuery{
		From: from, To: to, NodeID: "node-1", Limit: 25, Offset: 50,
	})
	if err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	if !reader.traffic.From.Equal(from) || !reader.traffic.To.Equal(to) {
		t.Fatalf("reader window = (%v, %v), want (%v, %v)", reader.traffic.From, reader.traffic.To, from, to)
	}
	if reader.traffic.NodeID != "node-1" {
		t.Fatalf("reader node = %q, want node-1", reader.traffic.NodeID)
	}
	if page.Limit != 25 || page.Offset != 50 || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v, want limit 25, offset 50, one item", page)
	}
}

func TestEventReadsRejectInvalidWindow(t *testing.T) {
	svc := &Events{reader: &stubEventReader{}}
	from := time.Now().UTC()
	to := from.Add(-time.Hour)

	if _, err := svc.Flows(context.Background(), domain.FlowQuery{From: from, To: to}); err == nil {
		t.Fatal("Flows accepted a reversed window")
	}
	if _, err := svc.DNS(context.Background(), domain.DNSQuery{From: from, To: to}); err == nil {
		t.Fatal("DNS accepted a reversed window")
	}
	if _, err := svc.Blocks(context.Background(), domain.BlockQuery{From: from, To: to}); err == nil {
		t.Fatal("Blocks accepted a reversed window")
	}
	if _, err := svc.Traffic(context.Background(), domain.TrafficQuery{From: from, To: to}); err == nil {
		t.Fatal("Traffic accepted a reversed window")
	}
}
