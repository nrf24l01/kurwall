package service

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/id"
)

const (
	maxStringLen     = 512
	maxAnswers       = 32
	maxEventSkew     = 24 * time.Hour
	maxQueryRange    = 90 * 24 * time.Hour
	earliestEvent    = 946684800 // 2000-01-01
	asnLookupTimeout = 2 * time.Second
)

// EventPublisher fans persisted events out to live dashboard clients.
type EventPublisher interface {
	PublishEvents(nodeID string, events []domain.Event)
}

// Events ingests agent batches and serves event history queries.
type Events struct {
	repo         domain.Repository
	writer       domain.EventWriter
	reader       domain.EventReader
	publisher    EventPublisher
	maxBatch     int
	dedupTimeout time.Duration
}

// NewEvents builds the event service.
func NewEvents(repo domain.Repository, writer domain.EventWriter, reader domain.EventReader, publisher EventPublisher, maxBatch int, dedupTimeout time.Duration) *Events {
	return &Events{repo: repo, writer: writer, reader: reader, publisher: publisher, maxBatch: maxBatch, dedupTimeout: dedupTimeout}
}

// Ingest persists an event batch exactly once per batch ID in the common path.
func (s *Events) Ingest(ctx context.Context, nodeID, batchID string, events []*kurwallv1.NetworkEvent) (bool, error) {
	if batchID == "" || len(batchID) > 64 {
		return false, apperr.Validation("batch_id is required and must be at most 64 characters")
	}
	if len(events) > s.maxBatch {
		return false, apperr.Validation("batch exceeds the maximum number of events")
	}
	if len(events) == 0 {
		return false, nil
	}
	converted := make([]domain.Event, 0, len(events))
	for _, raw := range events {
		ev, err := toDomainEvent(nodeID, raw)
		if err != nil {
			return false, err
		}
		converted = append(converted, ev)
	}

	claim, err := s.repo.ClaimBatch(ctx, nodeID, batchID, time.Now().UTC(), s.dedupTimeout)
	if err != nil {
		// PostgreSQL outages must not make the agent discard buffered events.
		return false, apperr.Unavailable("STATE_STORE_UNAVAILABLE", "event state store is unavailable", err)
	}
	if claim.Duplicate {
		return true, nil
	}
	if !claim.Claimed {
		return false, apperr.Unavailable("BATCH_IN_FLIGHT", "batch is already being processed", nil)
	}
	if err := s.writer.InsertBatch(ctx, nodeID, converted); err != nil {
		_ = s.repo.ReleaseBatch(ctx, nodeID, batchID)
		return false, apperr.Unavailable("EVENT_STORE_UNAVAILABLE", "event storage is temporarily unavailable", err)
	}
	// Enrich live frames after persistence so history stays the source of truth;
	// ASN lookup failures only omit the optional fields.
	if s.publisher != nil {
		// ASN data is best-effort display enrichment: a catalog replace can
		// hold locks on the ASN tables, so bound the lookup instead of letting
		// it delay the batch acknowledgement on the stream context.
		lookupCtx, cancel := context.WithTimeout(ctx, asnLookupTimeout)
		enrichTrafficASN(converted, s.asnInfo(lookupCtx, trafficDestinationIPs(converted)))
		cancel()
		s.publisher.PublishEvents(nodeID, converted)
	}
	if err := s.repo.CommitBatch(ctx, nodeID, batchID, time.Now().UTC()); err != nil {
		// The events are persisted; acknowledging avoids duplicate delivery.
		zerolog.Ctx(ctx).Error().Err(err).Str("batch_id", batchID).Msg("failed to commit event batch claim")
	}
	return false, nil
}

func toDomainEvent(nodeID string, raw *kurwallv1.NetworkEvent) (domain.Event, error) {
	if raw == nil {
		return domain.Event{}, apperr.Validation("event must not be null")
	}
	eventID := strings.TrimSpace(raw.GetEventId())
	if eventID == "" {
		eventID = id.NewV7()
	}
	if len(eventID) > 64 {
		return domain.Event{}, apperr.Validation("event_id must be at most 64 characters")
	}
	switch payload := raw.Event.(type) {
	case *kurwallv1.NetworkEvent_Flow:
		flow, err := toFlow(nodeID, eventID, payload.Flow)
		return domain.Event{ID: eventID, Kind: "flow", Flow: flow}, err
	case *kurwallv1.NetworkEvent_Dns:
		dns, err := toDNS(nodeID, eventID, payload.Dns)
		return domain.Event{ID: eventID, Kind: "dns", DNS: dns}, err
	case *kurwallv1.NetworkEvent_Block:
		block, err := toBlock(nodeID, eventID, payload.Block)
		return domain.Event{ID: eventID, Kind: "block", Block: block}, err
	default:
		return domain.Event{}, apperr.Validation("event payload is missing")
	}
}

func toFlow(nodeID, eventID string, f *kurwallv1.FlowEvent) (*domain.FlowRecord, error) {
	if f == nil {
		return nil, apperr.Validation("flow payload is missing")
	}
	src, err := requiredIP(f.GetSrcIp())
	if err != nil {
		return nil, err
	}
	dst, err := requiredIP(f.GetDstIp())
	if err != nil {
		return nil, err
	}
	started, ended, err := eventTimes(f.GetStartedAtUnixNano(), f.GetEndedAtUnixNano())
	if err != nil {
		return nil, err
	}
	if f.GetSrcPort() > 65535 || f.GetDstPort() > 65535 {
		return nil, apperr.Validation("flow port is out of range")
	}
	if f.GetProtocol() > 255 {
		return nil, apperr.Validation("flow protocol is out of range")
	}
	duration := uint32(0)
	if ms := ended.Sub(started).Milliseconds(); ms > 0 && ms < 1<<32 {
		duration = uint32(ms)
	}
	return &domain.FlowRecord{
		NodeID: nodeID, EventID: eventID, StartedAt: started, EndedAt: ended, DurationMS: duration,
		SrcIP: src, DstIP: dst, SrcPort: uint16(f.GetSrcPort()), DstPort: uint16(f.GetDstPort()),
		Protocol: uint8(f.GetProtocol()), Domain: cleanHostname(f.GetDomain()), SNI: cleanHostname(f.GetSni()),
		ContainerID: cleanString(f.GetContainerId()), ContainerName: cleanString(f.GetContainerName()),
		BytesIn: f.GetBytesIn(), BytesOut: f.GetBytesOut(),
		PacketsIn: f.GetPacketsIn(), PacketsOut: f.GetPacketsOut(),
		Blocked: f.GetBlocked(), RuleID: cleanString(f.GetRuleId()), Reason: cleanString(f.GetReason()),
		Network: cleanString(f.GetNetwork()),
	}, nil
}

func toDNS(nodeID, eventID string, d *kurwallv1.DnsEvent) (*domain.DNSRecord, error) {
	if d == nil {
		return nil, apperr.Validation("dns payload is missing")
	}
	at, err := singleTime(d.GetAtUnixNano())
	if err != nil {
		return nil, err
	}
	client, err := requiredIP(d.GetClientIp())
	if err != nil {
		return nil, err
	}
	qname := cleanHostname(d.GetQname())
	if qname == "" {
		return nil, apperr.Validation("dns qname is required")
	}
	if d.GetQtype() > 65535 || d.GetRcode() > 15 {
		return nil, apperr.Validation("dns qtype or rcode is out of range")
	}
	answers := make([]string, 0, len(d.GetAnswers()))
	for _, a := range d.GetAnswers() {
		if len(answers) >= maxAnswers {
			break
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(a))
		if err == nil {
			answers = append(answers, addr.Unmap().String())
		}
	}
	return &domain.DNSRecord{
		NodeID: nodeID, EventID: eventID, At: at, ClientIP: client, ClientPort: cleanString(d.GetClientPort()),
		QName: qname, QType: d.GetQtype(), RCode: d.GetRcode(), Answers: answers,
		Blocked: d.GetBlocked(), RuleID: cleanString(d.GetRuleId()),
		ContainerID: cleanString(d.GetContainerId()), ContainerName: cleanString(d.GetContainerName()),
		LatencyMS: d.GetLatencyMs(),
	}, nil
}

func toBlock(nodeID, eventID string, b *kurwallv1.BlockEvent) (*domain.BlockRecord, error) {
	if b == nil {
		return nil, apperr.Validation("block payload is missing")
	}
	at, err := singleTime(b.GetAtUnixNano())
	if err != nil {
		return nil, err
	}
	switch b.GetKind() {
	case "ip", "cidr", "domain", "asn", "dns":
	default:
		return nil, apperr.Validation("block kind must be ip, cidr, domain or dns")
	}
	if b.GetSrcPort() > 65535 || b.GetDstPort() > 65535 || b.GetProtocol() > 255 {
		return nil, apperr.Validation("block port or protocol is out of range")
	}
	src, _ := optionalIP(b.GetSrcIp())
	dst, _ := optionalIP(b.GetDstIp())
	return &domain.BlockRecord{
		NodeID: nodeID, EventID: eventID, At: at, Kind: b.GetKind(), Target: cleanString(b.GetTarget()),
		SrcIP: src, DstIP: dst, SrcPort: uint16(b.GetSrcPort()), DstPort: uint16(b.GetDstPort()),
		Protocol: uint8(b.GetProtocol()), Domain: cleanHostname(b.GetDomain()),
		ContainerID: cleanString(b.GetContainerId()), ContainerName: cleanString(b.GetContainerName()),
		RuleID: cleanString(b.GetRuleId()), Reason: cleanString(b.GetReason()),
	}, nil
}

// trafficDestinationIPs returns destination addresses from flow and block
// events. DNS events carry no destination and are intentionally excluded.
func trafficDestinationIPs(events []domain.Event) []string {
	ips := make([]string, 0, len(events))
	for i := range events {
		switch {
		case events[i].Flow != nil:
			if events[i].Flow.DstIP != "" {
				ips = append(ips, events[i].Flow.DstIP)
			}
		case events[i].Block != nil:
			if events[i].Block.DstIP != "" {
				ips = append(ips, events[i].Block.DstIP)
			}
		}
	}
	return ips
}

// enrichTrafficASN attaches resolved ASN data to flow and block events in
// place. Destinations without a match keep the zero value so JSON omits them.
func enrichTrafficASN(events []domain.Event, info map[string]domain.ASNInfo) {
	if len(info) == 0 {
		return
	}
	for i := range events {
		switch {
		case events[i].Flow != nil:
			if asn, ok := info[events[i].Flow.DstIP]; ok {
				events[i].Flow.DstASN = asn.ASN
				events[i].Flow.DstCountry = asn.Country
			}
		case events[i].Block != nil:
			if asn, ok := info[events[i].Block.DstIP]; ok {
				events[i].Block.DstASN = asn.ASN
				events[i].Block.DstCountry = asn.Country
			}
		}
	}
}

func requiredIP(raw string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "", apperr.Validation("event IP address is invalid")
	}
	return addr.Unmap().String(), nil
}

func optionalIP(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return requiredIP(raw)
}

func eventTimes(startRaw, endRaw int64) (time.Time, time.Time, error) {
	started := time.Unix(0, startRaw).UTC()
	ended := time.Unix(0, endRaw).UTC()
	if endRaw == 0 {
		ended = started
	}
	now := time.Now().UTC()
	if startRaw <= 0 || started.Year() < 2000 || started.After(now.Add(maxEventSkew)) {
		return time.Time{}, time.Time{}, apperr.Validation("flow start time is out of range")
	}
	if ended.Before(started) || ended.After(now.Add(maxEventSkew)) {
		return time.Time{}, time.Time{}, apperr.Validation("flow end time is out of range")
	}
	return started, ended, nil
}

func singleTime(raw int64) (time.Time, error) {
	at := time.Unix(0, raw).UTC()
	if raw <= 0 || at.Year() < 2000 || at.After(time.Now().UTC().Add(maxEventSkew)) {
		return time.Time{}, apperr.Validation("event time is out of range")
	}
	return at, nil
}

func cleanString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStringLen {
		return s[:maxStringLen]
	}
	return s
}

func cleanHostname(s string) string {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if len(s) > 253 {
		return s[:253]
	}
	return s
}

// Flows returns connection history.
func (s *Events) Flows(ctx context.Context, q domain.FlowQuery) (domain.Page[domain.FlowRecord], error) {
	from, to, err := normalizeWindow(q.From, q.To, time.Hour)
	if err != nil {
		return domain.Page[domain.FlowRecord]{}, err
	}
	q.From, q.To = from, to
	items, total, err := s.reader.Flows(ctx, q)
	if err != nil {
		return domain.Page[domain.FlowRecord]{}, err
	}
	return domain.Page[domain.FlowRecord]{Items: items, Total: total, Limit: pageLimit(q.Limit), Offset: q.Offset}, nil
}

// DNS returns DNS history.
func (s *Events) DNS(ctx context.Context, q domain.DNSQuery) (domain.Page[domain.DNSRecord], error) {
	from, to, err := normalizeWindow(q.From, q.To, time.Hour)
	if err != nil {
		return domain.Page[domain.DNSRecord]{}, err
	}
	q.From, q.To = from, to
	items, total, err := s.reader.DNS(ctx, q)
	if err != nil {
		return domain.Page[domain.DNSRecord]{}, err
	}
	return domain.Page[domain.DNSRecord]{Items: items, Total: total, Limit: pageLimit(q.Limit), Offset: q.Offset}, nil
}

// Blocks returns blocking history.
func (s *Events) Blocks(ctx context.Context, q domain.BlockQuery) (domain.Page[domain.BlockRecord], error) {
	from, to, err := normalizeWindow(q.From, q.To, time.Hour)
	if err != nil {
		return domain.Page[domain.BlockRecord]{}, err
	}
	q.From, q.To = from, to
	items, total, err := s.reader.Blocks(ctx, q)
	if err != nil {
		return domain.Page[domain.BlockRecord]{}, err
	}
	return domain.Page[domain.BlockRecord]{Items: items, Total: total, Limit: pageLimit(q.Limit), Offset: q.Offset}, nil
}

// Traffic returns allowed connections and blocking decisions in one feed.
func (s *Events) Traffic(ctx context.Context, q domain.TrafficQuery) (domain.Page[domain.TrafficEvent], error) {
	from, to, err := normalizeWindow(q.From, q.To, time.Hour)
	if err != nil {
		return domain.Page[domain.TrafficEvent]{}, err
	}
	q.From, q.To = from, to
	items, total, err := s.reader.Traffic(ctx, q)
	if err != nil {
		return domain.Page[domain.TrafficEvent]{}, err
	}
	dstIPs := make([]string, 0, len(items))
	for _, item := range items {
		if item.DstIP != "" {
			dstIPs = append(dstIPs, item.DstIP)
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, asnLookupTimeout)
	info := s.asnInfo(lookupCtx, dstIPs)
	cancel()
	for i := range items {
		if asn, ok := info[items[i].DstIP]; ok {
			items[i].DstASN = asn.ASN
			items[i].DstCountry = asn.Country
		}
	}
	return domain.Page[domain.TrafficEvent]{Items: items, Total: total, Limit: pageLimit(q.Limit), Offset: q.Offset}, nil
}

// asnInfo resolves ASN data for the unique, normalized IPs in one lookup.
// Lookup failures are logged and ignored so event reads stay available
// without ASN data.
func (s *Events) asnInfo(ctx context.Context, ips []string) map[string]domain.ASNInfo {
	out := make(map[string]domain.ASNInfo)
	if s.repo == nil || len(ips) == 0 {
		return out
	}
	seen := make(map[string]struct{}, len(ips))
	unique := make([]string, 0, len(ips))
	for _, raw := range ips {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		ip := addr.Unmap().String()
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		unique = append(unique, ip)
	}
	if len(unique) == 0 {
		return out
	}
	info, err := s.repo.LookupASNForIPs(ctx, unique)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Int("ips", len(unique)).Msg("failed to look up ASN for event IPs")
		return out
	}
	if info == nil {
		return out
	}
	return info
}

// Stats aggregates dashboard counters.
func (s *Events) Stats(ctx context.Context, from, to time.Time) (domain.Stats, error) {
	from, to, err := normalizeWindow(from, to, 24*time.Hour)
	if err != nil {
		return domain.Stats{}, err
	}
	return s.reader.Stats(ctx, from, to)
}

// Health reports event storage availability.
func (s *Events) Health(ctx context.Context) error {
	return s.reader.Health(ctx)
}

func normalizeWindow(from, to time.Time, defaultRange time.Duration) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-defaultRange)
	}
	from, to = from.UTC(), to.UTC()
	if from.After(to) {
		return time.Time{}, time.Time{}, apperr.Validation("from must be before to")
	}
	if to.Sub(from) > maxQueryRange {
		return time.Time{}, time.Time{}, apperr.Validation("time range must not exceed 90 days")
	}
	if to.After(now.Add(maxEventSkew)) {
		return time.Time{}, time.Time{}, apperr.Validation("to must not be in the future")
	}
	return from, to, nil
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 500 {
		return 500
	}
	return limit
}
