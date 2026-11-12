package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

// stubASNRepo overrides only the ASN lookup; the embedded nil interface
// satisfies the rest of domain.Repository for these focused tests.
type stubASNRepo struct {
	domain.Repository
	calls [][]string
	info  map[string]domain.ASNInfo
	err   error
	// block makes the lookup wait for its context deadline, simulating a
	// catalog replace holding locks on the ASN tables.
	block bool
}

func (s *stubASNRepo) LookupASNForIPs(ctx context.Context, ips []string) (map[string]domain.ASNInfo, error) {
	s.calls = append(s.calls, ips)
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.info, s.err
}

func (s *stubASNRepo) ClaimBatch(context.Context, string, string, time.Time, time.Duration) (domain.Claim, error) {
	return domain.Claim{Claimed: true}, nil
}

func (s *stubASNRepo) CommitBatch(context.Context, string, string, time.Time) error { return nil }

func (s *stubASNRepo) ReleaseBatch(context.Context, string, string) error { return nil }

type stubWriter struct {
	events []domain.Event
}

func (s *stubWriter) InsertBatch(_ context.Context, _ string, events []domain.Event) error {
	s.events = cloneEvents(events)
	return nil
}

func (s *stubWriter) Health(context.Context) error { return nil }

type stubPublisher struct {
	events []domain.Event
}

func (s *stubPublisher) PublishEvents(_ string, events []domain.Event) {
	s.events = cloneEvents(events)
}

// cloneEvents deep-copies event payloads so stubs observe values at call time
// instead of later in-place ASN enrichment of the shared records.
func cloneEvents(events []domain.Event) []domain.Event {
	out := make([]domain.Event, len(events))
	for i, ev := range events {
		out[i] = ev
		if ev.Flow != nil {
			flow := *ev.Flow
			out[i].Flow = &flow
		}
		if ev.DNS != nil {
			dns := *ev.DNS
			dns.Answers = append([]string(nil), ev.DNS.Answers...)
			out[i].DNS = &dns
		}
		if ev.Block != nil {
			block := *ev.Block
			out[i].Block = &block
		}
	}
	return out
}

func TestTrafficDestinationIPs(t *testing.T) {
	events := []domain.Event{
		{Kind: "flow", Flow: &domain.FlowRecord{DstIP: "203.0.113.10"}},
		{Kind: "flow", Flow: &domain.FlowRecord{}},
		{Kind: "block", Block: &domain.BlockRecord{DstIP: "2001:db8::1"}},
		{Kind: "block", Block: &domain.BlockRecord{}},
		{Kind: "dns", DNS: &domain.DNSRecord{QName: "example.com"}},
	}

	got := trafficDestinationIPs(events)
	want := []string{"203.0.113.10", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("trafficDestinationIPs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("trafficDestinationIPs = %v, want %v", got, want)
		}
	}
}

func TestEnrichTrafficASN(t *testing.T) {
	info := map[string]domain.ASNInfo{
		"203.0.113.10": {ASN: 64500, Country: "DE"},
		"198.51.100.7": {ASN: 64501, Country: "US"},
	}
	events := []domain.Event{
		{Kind: "flow", Flow: &domain.FlowRecord{DstIP: "203.0.113.10"}},
		{Kind: "block", Block: &domain.BlockRecord{DstIP: "198.51.100.7"}},
		{Kind: "dns", DNS: &domain.DNSRecord{QName: "example.com"}},
	}
	unknown := domain.FlowRecord{DstIP: "192.0.2.99"}
	events = append(events, domain.Event{Kind: "flow", Flow: &unknown})

	enrichTrafficASN(events, info)

	if events[0].Flow.DstASN != 64500 || events[0].Flow.DstCountry != "DE" {
		t.Fatalf("flow enrichment = (%d, %q), want (64500, \"DE\")", events[0].Flow.DstASN, events[0].Flow.DstCountry)
	}
	if events[1].Block.DstASN != 64501 || events[1].Block.DstCountry != "US" {
		t.Fatalf("block enrichment = (%d, %q), want (64501, \"US\")", events[1].Block.DstASN, events[1].Block.DstCountry)
	}
	if events[3].Flow.DstASN != 0 || events[3].Flow.DstCountry != "" {
		t.Fatalf("unknown destination gained ASN data: %+v", events[3].Flow)
	}

	// Empty lookup results must leave the batch untouched.
	enrichTrafficASN(events[:1], nil)
	if events[0].Flow.DstASN != 64500 {
		t.Fatalf("empty lookup cleared existing ASN data: %+v", events[0].Flow)
	}
}

func TestASNInfoNormalizesAndLogsFailure(t *testing.T) {
	repo := &stubASNRepo{info: map[string]domain.ASNInfo{"203.0.113.10": {ASN: 64500}}}
	svc := &Events{repo: repo}
	got := svc.asnInfo(context.Background(), []string{" 203.0.113.10 ", "::ffff:203.0.113.10", "not-an-ip"})
	if len(got) != 1 || got["203.0.113.10"].ASN != 64500 {
		t.Fatalf("asnInfo = %v, want one entry for 203.0.113.10", got)
	}
	if len(repo.calls) != 1 || len(repo.calls[0]) != 1 || repo.calls[0][0] != "203.0.113.10" {
		t.Fatalf("lookup calls = %v, want one normalized unique IP", repo.calls)
	}

	var log bytes.Buffer
	ctx := zerolog.New(&log).Level(zerolog.WarnLevel).WithContext(context.Background())
	failing := &Events{repo: &stubASNRepo{err: errors.New("postgres down")}}
	if got := failing.asnInfo(ctx, []string{"203.0.113.10"}); len(got) != 0 {
		t.Fatalf("failed lookup returned %v, want empty map", got)
	}
	if !strings.Contains(log.String(), "failed to look up ASN") {
		t.Fatalf("lookup failure was not logged: %q", log.String())
	}
}

func TestIngestEnrichesPublishedEvents(t *testing.T) {
	now := time.Now().UnixNano()
	flowEvent := func() *kurwallv1.NetworkEvent {
		return &kurwallv1.NetworkEvent{
			EventId: "evt-1",
			Event: &kurwallv1.NetworkEvent_Flow{Flow: &kurwallv1.FlowEvent{
				SrcIp: "10.0.0.1", DstIp: "203.0.113.10",
				StartedAtUnixNano: now, EndedAtUnixNano: now,
			}},
		}
	}
	cases := []struct {
		name        string
		repo        *stubASNRepo
		wantASN     int64
		wantCountry string
	}{
		{
			name:        "resolved",
			repo:        &stubASNRepo{info: map[string]domain.ASNInfo{"203.0.113.10": {ASN: 64500, Country: "DE"}}},
			wantASN:     64500,
			wantCountry: "DE",
		},
		{
			name: "lookup failure still publishes",
			repo: &stubASNRepo{err: errors.New("postgres down")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer, publisher := &stubWriter{}, &stubPublisher{}
			svc := &Events{repo: tc.repo, writer: writer, publisher: publisher, maxBatch: 10, dedupTimeout: time.Minute}
			duplicate, err := svc.Ingest(context.Background(), "node-1", "batch-1", []*kurwallv1.NetworkEvent{flowEvent()})
			if err != nil || duplicate {
				t.Fatalf("Ingest = (%v, %v), want (false, nil)", duplicate, err)
			}
			if len(writer.events) != 1 {
				t.Fatalf("writer received %d events, want 1", len(writer.events))
			}
			if writer.events[0].Flow == nil || writer.events[0].Flow.DstASN != 0 || writer.events[0].Flow.DstCountry != "" {
				t.Fatalf("writer snapshot = %+v, want pre-enrichment values", writer.events[0].Flow)
			}
			if len(publisher.events) != 1 || publisher.events[0].Flow == nil {
				t.Fatalf("published events = %+v, want one flow", publisher.events)
			}
			// The publisher snapshots payloads at publish time, so this asserts
			// enrichment happened before PublishEvents, not afterwards.
			flow := publisher.events[0].Flow
			if flow.DstASN != tc.wantASN || flow.DstCountry != tc.wantCountry {
				t.Fatalf("published flow ASN = (%d, %q), want (%d, %q)", flow.DstASN, flow.DstCountry, tc.wantASN, tc.wantCountry)
			}
		})
	}
}

func TestIngestASNLookupTimeoutStillPublishes(t *testing.T) {
	now := time.Now().UnixNano()
	writer, publisher := &stubWriter{}, &stubPublisher{}
	repo := &stubASNRepo{block: true}
	svc := &Events{repo: repo, writer: writer, publisher: publisher, maxBatch: 10, dedupTimeout: time.Minute}

	started := time.Now()
	duplicate, err := svc.Ingest(context.Background(), "node-1", "batch-1", []*kurwallv1.NetworkEvent{{
		EventId: "evt-1",
		Event: &kurwallv1.NetworkEvent_Flow{Flow: &kurwallv1.FlowEvent{
			SrcIp: "10.0.0.1", DstIp: "203.0.113.10",
			StartedAtUnixNano: now, EndedAtUnixNano: now,
		}},
	}})
	elapsed := time.Since(started)
	if err != nil || duplicate {
		t.Fatalf("Ingest = (%v, %v), want (false, nil)", duplicate, err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Ingest took %s, want a bounded ASN lookup deadline", elapsed)
	}
	if len(writer.events) != 1 {
		t.Fatalf("writer received %d events, want 1", len(writer.events))
	}
	if len(publisher.events) != 1 || publisher.events[0].Flow == nil {
		t.Fatalf("published events = %+v, want one flow despite ASN timeout", publisher.events)
	}
	if publisher.events[0].Flow.DstASN != 0 {
		t.Fatalf("timed-out lookup enriched the flow: %+v", publisher.events[0].Flow)
	}
}

func TestFlowAndBlockASNJSON(t *testing.T) {
	flow, err := json.Marshal(domain.FlowRecord{DstASN: 64500, DstCountry: "DE"})
	if err != nil {
		t.Fatalf("marshal flow: %v", err)
	}
	for _, want := range []string{`"dstAsn":64500`, `"dstCountry":"DE"`} {
		if !strings.Contains(string(flow), want) {
			t.Fatalf("flow JSON %s is missing %s", flow, want)
		}
	}

	unresolved, err := json.Marshal(domain.FlowRecord{})
	if err != nil {
		t.Fatalf("marshal unresolved flow: %v", err)
	}
	if strings.Contains(string(unresolved), "dstAsn") || strings.Contains(string(unresolved), "dstCountry") {
		t.Fatalf("unresolved flow JSON should omit ASN fields: %s", unresolved)
	}

	block, err := json.Marshal(domain.BlockRecord{DstASN: 64501, DstCountry: "US"})
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	for _, want := range []string{`"dstAsn":64501`, `"dstCountry":"US"`} {
		if !strings.Contains(string(block), want) {
			t.Fatalf("block JSON %s is missing %s", block, want)
		}
	}
}
