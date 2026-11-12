package domain

import (
	"context"
	"time"
)

// Repository is the PostgreSQL persistence contract used by coordinator
// services. Implementations translate driver errors into apperr values.
type Repository interface {
	// Health checks database connectivity.
	Health(ctx context.Context) error

	// Nodes.
	UpsertNode(ctx context.Context, hello Hello, status string, at time.Time) (Node, error)
	ListNodes(ctx context.Context) ([]Node, error)
	GetNode(ctx context.Context, id string) (Node, error)
	UpdateNode(ctx context.Context, id string, update NodeUpdate) (Node, error)
	SetNodeStatus(ctx context.Context, id, status string, lastSeen *time.Time) error
	SetNodePolicyVersion(ctx context.Context, id string, version int64) error
	SetNodePolicies(ctx context.Context, nodeID string, policyIDs []string) error
	NodePolicyIDs(ctx context.Context, nodeID string) ([]string, error)
	ActiveNodeIDs(ctx context.Context) ([]string, error)

	// Policies and rules.
	ListPolicies(ctx context.Context) ([]Policy, error)
	GetPolicy(ctx context.Context, id string) (Policy, error)
	CreatePolicy(ctx context.Context, input PolicyInput) (Policy, error)
	UpdatePolicy(ctx context.Context, id string, input PolicyInput) (Policy, error)
	DeletePolicy(ctx context.Context, id string) error
	ListRules(ctx context.Context, policyID string) ([]Rule, error)
	CreateRule(ctx context.Context, policyID string, input RuleInput) (Rule, error)
	CreateRules(ctx context.Context, policyID string, inputs []RuleInput) ([]Rule, []string, error)
	UpdateRule(ctx context.Context, policyID, ruleID string, input RuleInput) (Rule, error)
	DeleteRule(ctx context.Context, policyID, ruleID string) error

	// ASN directory.
	ReplaceASNCatalog(ctx context.Context, entries []ASNEntry, ranges []ASNRange) error
	SearchASNCatalog(ctx context.Context, country, query string, limit, offset int) ([]ASNEntry, int64, error)
	ASNCountries(ctx context.Context) ([]ASNCountry, error)
	ASNCatalogStats(ctx context.Context) (ASNCatalogStats, error)
	CountASNRanges(ctx context.Context) (int64, error)
	LookupASNForIPs(ctx context.Context, ips []string) (map[string]ASNInfo, error)

	// ASN prefix cache.
	GetASNCache(ctx context.Context, asn int64) (ASNCache, bool, error)
	SaveASNCache(ctx context.Context, cache ASNCache) error
	ASNsInUse(ctx context.Context) ([]int64, error)

	// Revisions and snapshots.
	LatestRevision(ctx context.Context) (int64, error)
	CreateRevision(ctx context.Context, actor string) (int64, error)
	SnapshotForNode(ctx context.Context, nodeID string) (Snapshot, error)
	NodeIDsAffectedByPolicy(ctx context.Context, policyID string) ([]string, error)
	MarkStaleOffline(ctx context.Context, before time.Time) (int64, error)

	// Event batch idempotency.
	ClaimBatch(ctx context.Context, nodeID, batchID string, now time.Time, takeoverAfter time.Duration) (Claim, error)
	CommitBatch(ctx context.Context, nodeID, batchID string, now time.Time) error
	ReleaseBatch(ctx context.Context, nodeID, batchID string) error
	CleanupBatches(ctx context.Context, olderThan time.Time) (int64, error)
}

// Claim describes the outcome of an idempotency claim for an event batch.
type Claim struct {
	// Claimed is true when this caller must persist the batch.
	Claimed bool
	// Duplicate is true when an identical batch was already committed.
	Duplicate bool
}

// EventReader queries ClickHouse event storage.
type EventReader interface {
	Flows(ctx context.Context, f FlowQuery) ([]FlowRecord, uint64, error)
	DNS(ctx context.Context, f DNSQuery) ([]DNSRecord, uint64, error)
	Blocks(ctx context.Context, f BlockQuery) ([]BlockRecord, uint64, error)
	Traffic(ctx context.Context, f TrafficQuery) ([]TrafficEvent, uint64, error)
	Stats(ctx context.Context, from, to time.Time) (Stats, error)
	Health(ctx context.Context) error
}

// EventWriter persists event batches into ClickHouse.
type EventWriter interface {
	InsertBatch(ctx context.Context, nodeID string, events []Event) error
	Health(ctx context.Context) error
}

// Event is a transport-neutral representation of one network event.
type Event struct {
	ID    string       `json:"id"`
	Kind  string       `json:"kind"`
	Flow  *FlowRecord  `json:"flow,omitempty"`
	DNS   *DNSRecord   `json:"dns,omitempty"`
	Block *BlockRecord `json:"block,omitempty"`
}

// FlowRecord is one consolidated completed connection.
type FlowRecord struct {
	NodeID        string    `json:"nodeId"`
	EventID       string    `json:"eventId"`
	StartedAt     time.Time `json:"startedAt"`
	EndedAt       time.Time `json:"endedAt"`
	DurationMS    uint32    `json:"durationMs"`
	SrcIP         string    `json:"srcIp"`
	DstIP         string    `json:"dstIp"`
	SrcPort       uint16    `json:"srcPort"`
	DstPort       uint16    `json:"dstPort"`
	Protocol      uint8     `json:"protocol"`
	Domain        string    `json:"domain"`
	SNI           string    `json:"sni"`
	ContainerID   string    `json:"containerId"`
	ContainerName string    `json:"containerName"`
	BytesIn       uint64    `json:"bytesIn"`
	BytesOut      uint64    `json:"bytesOut"`
	PacketsIn     uint64    `json:"packetsIn"`
	PacketsOut    uint64    `json:"packetsOut"`
	Blocked       bool      `json:"blocked"`
	RuleID        string    `json:"ruleId"`
	Reason        string    `json:"reason"`
	Network       string    `json:"network"`
}

// DNSRecord preserves one DNS request.
type DNSRecord struct {
	NodeID        string    `json:"nodeId"`
	EventID       string    `json:"eventId"`
	At            time.Time `json:"at"`
	ClientIP      string    `json:"clientIp"`
	ClientPort    string    `json:"clientPort"`
	QName         string    `json:"qname"`
	QType         uint32    `json:"qtype"`
	RCode         uint32    `json:"rcode"`
	Answers       []string  `json:"answers"`
	Blocked       bool      `json:"blocked"`
	RuleID        string    `json:"ruleId"`
	ContainerID   string    `json:"containerId"`
	ContainerName string    `json:"containerName"`
	LatencyMS     uint32    `json:"latencyMs"`
}

// BlockRecord preserves one blocking decision, including kernel drops.
type BlockRecord struct {
	NodeID        string    `json:"nodeId"`
	EventID       string    `json:"eventId"`
	At            time.Time `json:"at"`
	Kind          string    `json:"kind"`
	Target        string    `json:"target"`
	SrcIP         string    `json:"srcIp"`
	DstIP         string    `json:"dstIp"`
	SrcPort       uint16    `json:"srcPort"`
	DstPort       uint16    `json:"dstPort"`
	Protocol      uint8     `json:"protocol"`
	Domain        string    `json:"domain"`
	ContainerID   string    `json:"containerId"`
	ContainerName string    `json:"containerName"`
	RuleID        string    `json:"ruleId"`
	Reason        string    `json:"reason"`
}

// TrafficEvent is one allowed connection or one blocking decision in the
// unified traffic view.
type TrafficEvent struct {
	Kind          string    `json:"kind"`
	EventID       string    `json:"eventId"`
	NodeID        string    `json:"nodeId"`
	At            time.Time `json:"at"`
	EndedAt       time.Time `json:"endedAt"`
	DurationMS    uint32    `json:"durationMs"`
	SrcIP         string    `json:"srcIp"`
	DstIP         string    `json:"dstIp"`
	SrcPort       uint16    `json:"srcPort"`
	DstPort       uint16    `json:"dstPort"`
	Protocol      uint8     `json:"protocol"`
	Domain        string    `json:"domain"`
	SNI           string    `json:"sni"`
	Target        string    `json:"target"`
	BlockKind     string    `json:"blockKind"`
	ContainerID   string    `json:"containerId"`
	ContainerName string    `json:"containerName"`
	BytesIn       uint64    `json:"bytesIn"`
	BytesOut      uint64    `json:"bytesOut"`
	PacketsIn     uint64    `json:"packetsIn"`
	PacketsOut    uint64    `json:"packetsOut"`
	Blocked       bool      `json:"blocked"`
	RuleID        string    `json:"ruleId"`
	Reason        string    `json:"reason"`
	DstASN        int64     `json:"dstAsn,omitempty"`
	DstCountry    string    `json:"dstCountry,omitempty"`
}

// TrafficQuery filters the unified traffic history.
type TrafficQuery struct {
	NodeID    string
	SrcIP     string
	DstIP     string
	IP        string
	Domain    string
	Container string
	Decision  string
	Protocol  *uint8
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// FlowQuery filters connection history.
type FlowQuery struct {
	NodeID    string
	SrcIP     string
	DstIP     string
	Domain    string
	Container string
	Decision  string
	Protocol  *uint8
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// DNSQuery filters DNS history.
type DNSQuery struct {
	NodeID    string
	ClientIP  string
	Domain    string
	Container string
	Blocked   *bool
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// BlockQuery filters blocking events.
type BlockQuery struct {
	NodeID    string
	Kind      string
	Target    string
	Domain    string
	Container string
	SrcIP     string
	DstIP     string
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// Page is a generic paginated result.
type Page[T any] struct {
	Items  []T    `json:"items"`
	Total  uint64 `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}
