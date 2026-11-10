// Package domain holds the coordinator's business entities and repository
// contract. It has no dependency on transport or storage packages.
package domain

import (
	"time"
)

// Node status values exposed by the API.
const (
	NodeStatusOnline  = "online"
	NodeStatusOffline = "offline"
	NodeStatusPending = "pending"
)

// Rule kinds supported by the filtering engine.
const (
	RuleKindIP     = "ip"
	RuleKindCIDR   = "cidr"
	RuleKindDomain = "domain"
	RuleKindASN    = "asn"
)

// Rule actions. Blocking is the only enforced action today; the column exists
// so allow-listing can be added without a schema rewrite.
const (
	RuleActionBlock = "block"
)

// Node is a monitored Linux server running the Kurwall agent.
type Node struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Hostname      string            `json:"hostname"`
	AgentVersion  string            `json:"agentVersion"`
	OS            string            `json:"os"`
	Kernel        string            `json:"kernel"`
	Addresses     []string          `json:"addresses"`
	Status        string            `json:"status"`
	LastSeenAt    *time.Time        `json:"lastSeenAt,omitempty"`
	Labels        map[string]string `json:"labels"`
	PolicyIDs     []string          `json:"policyIds"`
	PolicyVersion int64             `json:"policyVersion"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// Policy groups filtering rules and can be assigned to many nodes.
type Policy struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Enabled     bool         `json:"enabled"`
	RuleCount   int          `json:"ruleCount"`
	Rules       []Rule       `json:"rules,omitempty"`
	Nodes       []PolicyNode `json:"nodes"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

// PolicyNode is a node that receives a policy.
type PolicyNode struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	AgentVersion  string `json:"agentVersion"`
	PolicyVersion int64  `json:"policyVersion"`
}

// User is a dashboard/API account. PasswordHash is never serialized.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Session is a server-side login session.
type Session struct {
	ID         string
	UserID     string
	TokenHash  []byte
	CSRFHash   []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	RemoteIP   string
}

// DeployKey enrolls one node and optionally assigns policies to it.
type DeployKey struct {
	ID        string     `json:"id"`
	NodeName  string     `json:"nodeName"`
	TokenHash []byte     `json:"-"`
	PolicyIDs []string   `json:"policyIds"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	UsedByIP  string     `json:"usedByIp,omitempty"`
}

// Rule is a single IP, CIDR or domain blocking rule.
type Rule struct {
	ID         string    `json:"id"`
	PolicyID   string    `json:"policyId"`
	PolicyName string    `json:"policyName,omitempty"`
	Kind       string    `json:"kind"`
	Value      string    `json:"value"`
	Action     string    `json:"action"`
	Note       string    `json:"note"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	// Prefixes are the resolved CIDRs of an ASN rule, present in node
	// snapshots and ASN previews. Policy listings expose only the count.
	Prefixes    []string `json:"prefixes,omitempty"`
	PrefixCount int      `json:"prefixCount,omitempty"`
}

// ASNEntry is one row of the searchable ASN directory.
type ASNEntry struct {
	ASN         int64  `json:"asn"`
	Name        string `json:"name"`
	Country     string `json:"country"`
	PrefixCount int    `json:"prefixCount"`
}

// ASNCountry is an ASN directory country facet.
type ASNCountry struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

// ASNCatalogStats describes the local directory.
type ASNCatalogStats struct {
	Count     int64      `json:"count"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// ASNCache is the coordinator's cached prefix resolution for one ASN.
type ASNCache struct {
	ASN         int64
	Holder      string
	Prefixes    []string
	UpdatedAt   time.Time
	RefreshedAt time.Time
	Error       string
}

// PolicyMeta is the compact policy reference included in snapshots.
type PolicyMeta struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentSettings are inspection knobs pushed to agents with each snapshot.
type AgentSettings struct {
	TLSInspectorPorts []uint32 `json:"tlsInspectorPorts"`
	DNSInspection     bool     `json:"dnsInspection"`
	TLSInspection     bool     `json:"tlsInspection"`
	BatchMaxEvents    uint32   `json:"batchMaxEvents"`
	FlushIntervalMS   uint32   `json:"flushIntervalMs"`
}

// Snapshot is the effective, node-specific policy distributed to an agent.
type Snapshot struct {
	Version     int64         `json:"version"`
	NodeID      string        `json:"nodeId"`
	GeneratedAt time.Time     `json:"generatedAt"`
	Rules       []Rule        `json:"rules"`
	Policies    []PolicyMeta  `json:"policies"`
	Settings    AgentSettings `json:"settings"`
}

// Hello carries the information an agent reports when it connects.
type Hello struct {
	NodeID       string
	Hostname     string
	AgentVersion string
	OS           string
	Kernel       string
	Addresses    []string
}

// NodeUpdate carries user-editable node fields.
type NodeUpdate struct {
	Name   *string            `json:"name"`
	Labels *map[string]string `json:"labels"`
}

// PolicyInput is used to create or update a policy.
type PolicyInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// RuleInput is used to create or update a rule.
type RuleInput struct {
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Action  string `json:"action"`
	Note    string `json:"note"`
	Enabled bool   `json:"enabled"`
}

// Stats is the dashboard overview payload.
type Stats struct {
	Window          StatsWindow    `json:"window"`
	Flows           uint64         `json:"flows"`
	BlockedFlows    uint64         `json:"blockedFlows"`
	DNSQueries      uint64         `json:"dnsQueries"`
	BlockedDNS      uint64         `json:"blockedDns"`
	BlockEvents     uint64         `json:"blockEvents"`
	BytesOut        uint64         `json:"bytesOut"`
	BytesIn         uint64         `json:"bytesIn"`
	TopDomains      []CountItem    `json:"topDomains"`
	TopDestinations []CountItem    `json:"topDestinations"`
	TopContainers   []CountItem    `json:"topContainers"`
	Nodes           []NodeActivity `json:"nodes"`
}

type StatsWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type CountItem struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

type NodeActivity struct {
	NodeID       string    `json:"nodeId"`
	Flows        uint64    `json:"flows"`
	BlockedFlows uint64    `json:"blockedFlows"`
	DNSQueries   uint64    `json:"dnsQueries"`
	BytesOut     uint64    `json:"bytesOut"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
}
