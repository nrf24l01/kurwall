package clickhouse

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

func TestTrafficBranches(t *testing.T) {
	cases := []struct {
		decision string
		flows    bool
		blocks   bool
	}{
		{"", true, true},
		{"allowed", true, false},
		{"blocked", false, true},
	}
	for _, tc := range cases {
		flows, blocks := trafficBranches(tc.decision)
		if flows != tc.flows || blocks != tc.blocks {
			t.Fatalf("trafficBranches(%q) = (%t, %t), want (%t, %t)", tc.decision, flows, blocks, tc.flows, tc.blocks)
		}
	}
}

// trafficOutputName returns the output column name of a union branch
// expression: the alias when present, otherwise the unqualified column name.
func trafficOutputName(expr string) string {
	if i := strings.LastIndex(expr, " AS "); i >= 0 {
		return strings.TrimSpace(expr[i+4:])
	}
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		return expr[i+1:]
	}
	return expr
}

func TestTrafficColumnOrder(t *testing.T) {
	want := []string{
		"event_id", "kind", "node_id", "at", "ended_at", "duration_ms",
		"src_ip", "dst_ip", "src_port", "dst_port", "protocol",
		"domain", "sni", "target", "block_kind",
		"container_id", "container_name", "bytes_in", "bytes_out",
		"packets_in", "packets_out", "blocked", "rule_id", "reason", "network",
	}
	branches := []struct {
		name string
		cols []string
	}{
		{"flows", trafficFlowColumns},
		{"blocks", trafficBlockColumns},
	}
	for _, branch := range branches {
		if len(branch.cols) != len(want) {
			t.Fatalf("%s branch has %d columns, want %d", branch.name, len(branch.cols), len(want))
		}
		for i, expr := range branch.cols {
			if got := trafficOutputName(expr); got != want[i] {
				t.Fatalf("%s column %d = %q, want %q", branch.name, i+1, got, want[i])
			}
		}
	}

	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	query, _, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: from.Add(time.Hour)}, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}
	for _, branch := range branches {
		if !strings.Contains(query, strings.Join(branch.cols, ",\n\t\t\t")) {
			t.Fatalf("%s select list is missing or out of order in query:\n%s", branch.name, query)
		}
	}
}

func TestBuildTrafficQueryFilters(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	protocol := uint8(6)
	q := domain.TrafficQuery{
		NodeID:    "node-1",
		SrcIP:     "10.0.1.2",
		DstIP:     "8.8.8.8",
		IP:        "2001:4860:4860::8888",
		Domain:    "example.com",
		Container: "web",
		Protocol:  &protocol,
		From:      from,
		To:        to,
		Limit:     25,
		Offset:    50,
	}

	query, args, err := buildTrafficQuery(q, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}

	fragments := []string{
		"flows.blocked = 0",
		"block_events.at >= ?",
		"block_events.at <= ?",
		"positionCaseInsensitive(block_events.target, ?)",
		"positionCaseInsensitive(domain, ?)",
		"positionCaseInsensitive(sni, ?)",
		"(src_ip = toIPv6(?) OR dst_ip = toIPv6(?))",
		"src_ip = toIPv6(?)",
		"dst_ip = toIPv6(?)",
		"(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)",
		"node_id = ?",
		"protocol = ?",
		"ORDER BY at DESC, event_id DESC LIMIT 25 OFFSET 50",
	}
	for _, fragment := range fragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query is missing %q:\n%s", fragment, query)
		}
	}

	branchArgs := []any{
		from, to, "node-1", protocol, "web", "web",
		"10.0.1.2", "8.8.8.8", "2001:4860:4860::8888", "2001:4860:4860::8888",
		"example.com", "example.com",
	}
	wantArgs := append(append([]any{}, branchArgs...), branchArgs...)
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestBuildTrafficQueryBranches(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	cases := []struct {
		name     string
		decision string
		flows    bool
		blocks   bool
	}{
		{"any", "", true, true},
		{"allowed", "allowed", true, false},
		{"blocked", "blocked", false, true},
	}
	for _, tc := range cases {
		flows, blocks := trafficBranches(tc.decision)
		query, args, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: to}, flows, blocks)
		if err != nil {
			t.Fatalf("%s: buildTrafficQuery: %v", tc.name, err)
		}
		if got := strings.Count(query, "FROM flows"); (got == 1) != tc.flows {
			t.Fatalf("%s: FROM flows count = %d, want branch included = %t", tc.name, got, tc.flows)
		}
		if got := strings.Count(query, "FROM block_events"); (got == 1) != tc.blocks {
			t.Fatalf("%s: FROM block_events count = %d, want branch included = %t", tc.name, got, tc.blocks)
		}
		wantArgs := make([]any, 0, 4)
		if tc.flows {
			wantArgs = append(wantArgs, from, to)
		}
		if tc.blocks {
			wantArgs = append(wantArgs, from, to)
		}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("%s: args = %#v, want %#v", tc.name, args, wantArgs)
		}
	}
}

func TestBuildTrafficQueryExcludeLocal(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	query, _, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: to, ExcludeLocal: true}, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}
	for _, fragment := range []string{
		"NOT (",
		"isIPAddressInRange(src_ip, '::ffff:10.0.0.0/104')",
		"isIPAddressInRange(src_ip, '::ffff:100.64.0.0/106')",
		"isIPAddressInRange(src_ip, '::ffff:192.168.0.0/112')",
		"isIPAddressInRange(dst_ip, 'fc00::/7')",
		"isIPAddressInRange(dst_ip, 'fe80::/10')",
		"isIPAddressInRange(dst_ip, 'ff00::/8')",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("excludeLocal query is missing %q:\n%s", fragment, query)
		}
	}

	without, _, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: to}, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery without excludeLocal: %v", err)
	}
	if strings.Contains(without, "isIPAddressInRange") {
		t.Fatalf("excludeLocal predicate leaked into the default query:\n%s", without)
	}
}

func TestBuildTrafficQueryConditions(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	q := domain.TrafficQuery{
		From: from, To: to, Match: "all",
		Conditions: []domain.TrafficCondition{
			{Field: "ip.dst", Operator: "eq", Value: "8.8.8.8"},
			{Field: "decision", Operator: "eq", Value: "blocked", Negate: true},
			{Field: "domain", Operator: "contains", Value: "example"},
		},
	}
	query, args, err := buildTrafficQuery(q, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}
	for _, fragment := range []string{
		"SELECT * FROM (",
		" WHERE (",
		"dst_ip = toIPv6(?)",
		"NOT (blocked = ?)",
		"positionCaseInsensitive(domain, ?) > 0",
		"positionCaseInsensitive(sni, ?) > 0",
		"positionCaseInsensitive(target, ?) > 0",
		" AND ",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("condition query is missing %q:\n%s", fragment, query)
		}
	}
	if len(args) < 7 {
		t.Fatalf("args = %#v, want branch and condition arguments", args)
	}

	anyQuery, _, err := buildTrafficQuery(domain.TrafficQuery{
		From: from, To: to, Match: "any",
		Conditions: []domain.TrafficCondition{
			{Field: "protocol", Operator: "eq", Value: "tcp"},
			{Field: "port", Operator: "eq", Value: "443"},
		},
	}, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery any: %v", err)
	}
	if !strings.Contains(anyQuery, " OR ") {
		t.Fatalf("any query is missing OR:\n%s", anyQuery)
	}
	if !strings.Contains(anyQuery, "src_port = ?") || !strings.Contains(anyQuery, "dst_port = ?") {
		t.Fatalf("port condition is missing either side:\n%s", anyQuery)
	}

	for _, bad := range []domain.TrafficQuery{
		{From: from, To: to, Conditions: []domain.TrafficCondition{{Field: "nope", Operator: "eq", Value: "x"}}},
		{From: from, To: to, Conditions: []domain.TrafficCondition{{Field: "ip.dst", Operator: "contains", Value: "8.8.8.8"}}},
		{From: from, To: to, Conditions: []domain.TrafficCondition{{Field: "port", Operator: "eq", Value: "not-a-port"}}},
	} {
		if _, _, err := buildTrafficQuery(bad, true, true); err == nil {
			t.Fatalf("expected validation error for %+v", bad)
		}
	}
}

func TestBuildTrafficQueryTextCondition(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	query, _, err := buildTrafficQuery(domain.TrafficQuery{
		From: from, To: from.Add(time.Hour), Match: "all",
		Conditions: []domain.TrafficCondition{{Field: "text", Operator: "contains", Value: "cloudflare"}},
	}, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}
	for _, fragment := range []string{
		"positionCaseInsensitive(domain, ?) > 0",
		"positionCaseInsensitive(sni, ?) > 0",
		"positionCaseInsensitive(target, ?) > 0",
		"positionCaseInsensitive(reason, ?) > 0",
		"positionCaseInsensitive(container_name, ?) > 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("text condition query is missing %q:\n%s", fragment, query)
		}
	}
}

// TestEventWhereDateRange proves Flows, DNS and Blocks all filter on their
// event time column with the requested window instead of applying it to only
// one table or relying on the request default.
func TestEventWhereDateRange(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(90 * time.Minute)

	flowWhere, err := flowsWhere(domain.FlowQuery{From: from, To: to})
	if err != nil {
		t.Fatalf("flowsWhere: %v", err)
	}
	wantFlow := []string{"started_at >= ?", "started_at <= ?"}
	if !reflect.DeepEqual(flowWhere.clauses, wantFlow) {
		t.Fatalf("flow clauses = %#v, want %#v", flowWhere.clauses, wantFlow)
	}
	if !reflect.DeepEqual(flowWhere.args, []any{from, to}) {
		t.Fatalf("flow args = %#v, want the request window", flowWhere.args)
	}

	dnsWhere, err := dnsWhere(domain.DNSQuery{From: from, To: to})
	if err != nil {
		t.Fatalf("dnsWhere: %v", err)
	}
	wantDNS := []string{"at >= ?", "at <= ?"}
	if !reflect.DeepEqual(dnsWhere.clauses, wantDNS) {
		t.Fatalf("dns clauses = %#v, want %#v", dnsWhere.clauses, wantDNS)
	}
	if !reflect.DeepEqual(dnsWhere.args, []any{from, to}) {
		t.Fatalf("dns args = %#v, want the request window", dnsWhere.args)
	}

	blockWhere, err := blocksWhere(domain.BlockQuery{From: from, To: to})
	if err != nil {
		t.Fatalf("blocksWhere: %v", err)
	}
	wantBlocks := []string{"at >= ?", "at <= ?"}
	if !reflect.DeepEqual(blockWhere.clauses, wantBlocks) {
		t.Fatalf("block clauses = %#v, want %#v", blockWhere.clauses, wantBlocks)
	}
	if !reflect.DeepEqual(blockWhere.args, []any{from, to}) {
		t.Fatalf("block args = %#v, want the request window", blockWhere.args)
	}
}

func TestFlowsWhereAppliesFilters(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	protocol := uint8(17)

	w, err := flowsWhere(domain.FlowQuery{
		NodeID: "node-1", SrcIP: "10.0.0.1", DstIP: "::ffff:8.8.8.8",
		Domain: "Example.COM", Container: "web", Decision: "blocked",
		Protocol: &protocol, From: from, To: to,
	})
	if err != nil {
		t.Fatalf("flowsWhere: %v", err)
	}
	wantClauses := []string{
		"started_at >= ?",
		"started_at <= ?",
		"node_id = ?",
		"src_ip = toIPv6(?)",
		"dst_ip = toIPv6(?)",
		"(positionCaseInsensitive(domain, ?) > 0 OR positionCaseInsensitive(sni, ?) > 0)",
		"(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)",
		"blocked = 1",
		"protocol = ?",
	}
	if !reflect.DeepEqual(w.clauses, wantClauses) {
		t.Fatalf("clauses = %#v, want %#v", w.clauses, wantClauses)
	}
	wantArgs := []any{from, to, "node-1", "10.0.0.1", "8.8.8.8", "Example.COM", "Example.COM", "web", "web", protocol}
	if !reflect.DeepEqual(w.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", w.args, wantArgs)
	}

	allowed, err := flowsWhere(domain.FlowQuery{From: from, To: to, Decision: "allowed"})
	if err != nil {
		t.Fatalf("flowsWhere allowed: %v", err)
	}
	if !strings.Contains(strings.Join(allowed.clauses, " AND "), "blocked = 0") {
		t.Fatalf("allowed decision clauses = %#v, want blocked = 0", allowed.clauses)
	}

	if _, err := flowsWhere(domain.FlowQuery{From: from, To: to, SrcIP: "not-an-ip"}); err == nil {
		t.Fatal("expected validation error for an invalid source IP")
	}
}

func TestDNSWhereAppliesFilters(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	blocked := true

	w, err := dnsWhere(domain.DNSQuery{
		NodeID: "node-1", ClientIP: "::ffff:10.0.0.7", Domain: "Example.COM",
		Container: "resolver", Blocked: &blocked, From: from, To: to,
	})
	if err != nil {
		t.Fatalf("dnsWhere: %v", err)
	}
	wantClauses := []string{
		"at >= ?",
		"at <= ?",
		"node_id = ?",
		"client_ip = toIPv6(?)",
		"positionCaseInsensitive(qname, ?) > 0",
		"(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)",
		"blocked = 1",
	}
	if !reflect.DeepEqual(w.clauses, wantClauses) {
		t.Fatalf("clauses = %#v, want %#v", w.clauses, wantClauses)
	}
	wantArgs := []any{from, to, "node-1", "10.0.0.7", "Example.COM", "resolver", "resolver"}
	if !reflect.DeepEqual(w.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", w.args, wantArgs)
	}

	allowed := false
	w, err = dnsWhere(domain.DNSQuery{From: from, To: to, Blocked: &allowed})
	if err != nil {
		t.Fatalf("dnsWhere allowed: %v", err)
	}
	if !reflect.DeepEqual(w.clauses[len(w.clauses)-1], "blocked = 0") {
		t.Fatalf("allowed decision clause = %q, want blocked = 0", w.clauses[len(w.clauses)-1])
	}

	w, err = dnsWhere(domain.DNSQuery{From: from, To: to})
	if err != nil {
		t.Fatalf("dnsWhere unfiltered: %v", err)
	}
	for _, clause := range w.clauses {
		if strings.HasPrefix(clause, "blocked") {
			t.Fatalf("unset blocked filter produced %q", clause)
		}
	}

	if _, err := dnsWhere(domain.DNSQuery{From: from, To: to, ClientIP: "not-an-ip"}); err == nil {
		t.Fatal("expected validation error for an invalid client IP")
	}
}

func TestBlocksWhereAppliesFilters(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	w, err := blocksWhere(domain.BlockQuery{
		NodeID: "node-1", Kind: "dns", Target: "ads.example", Domain: "Example.COM",
		Container: "web", SrcIP: "10.0.0.1", DstIP: "::ffff:1.1.1.1", From: from, To: to,
	})
	if err != nil {
		t.Fatalf("blocksWhere: %v", err)
	}
	wantClauses := []string{
		"at >= ?",
		"at <= ?",
		"node_id = ?",
		"kind = ?",
		"positionCaseInsensitive(target, ?) > 0",
		"positionCaseInsensitive(domain, ?) > 0",
		"(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)",
		"src_ip = toIPv6(?)",
		"dst_ip = toIPv6(?)",
	}
	if !reflect.DeepEqual(w.clauses, wantClauses) {
		t.Fatalf("clauses = %#v, want %#v", w.clauses, wantClauses)
	}
	wantArgs := []any{from, to, "node-1", "dns", "ads.example", "Example.COM", "web", "web", "10.0.0.1", "1.1.1.1"}
	if !reflect.DeepEqual(w.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", w.args, wantArgs)
	}

	if _, err := blocksWhere(domain.BlockQuery{From: from, To: to, DstIP: "not-an-ip"}); err == nil {
		t.Fatal("expected validation error for an invalid destination IP")
	}
}

// TestTrafficSharedFiltersApplyToBothBranches proves node, protocol, container,
// address and exclude-local filters are emitted once for the flow branch and
// once for the block branch, with the same argument values in the same order.
func TestTrafficSharedFiltersApplyToBothBranches(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	protocol := uint8(6)
	q := domain.TrafficQuery{
		NodeID: "node-1", SrcIP: "10.0.0.1", DstIP: "8.8.8.8", IP: "1.1.1.1",
		Domain: "example.com", Container: "web", ExcludeLocal: true,
		Protocol: &protocol, From: from, To: to,
	}

	query, args, err := buildTrafficQuery(q, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}
	for _, fragment := range []string{
		"node_id = ?",
		"protocol = ?",
		"(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)",
		"NOT (",
	} {
		if got := strings.Count(query, fragment); got != 2 {
			t.Fatalf("%q appears %d times, want once per union branch:\n%s", fragment, got, query)
		}
	}
	// The src filter and the "either endpoint" filter both mention src_ip, so
	// each branch contributes two matches for each endpoint.
	if got := strings.Count(query, "src_ip = toIPv6(?)"); got != 4 {
		t.Fatalf("src_ip equality appears %d times, want 4:\n%s", got, query)
	}
	if got := strings.Count(query, "dst_ip = toIPv6(?)"); got != 4 {
		t.Fatalf("dst_ip equality appears %d times, want 4:\n%s", got, query)
	}
	if got := strings.Count(query, "(positionCaseInsensitive(domain, ?) > 0 OR positionCaseInsensitive(sni, ?) > 0)"); got != 1 {
		t.Fatalf("flow domain filter appears %d times, want 1:\n%s", got, query)
	}
	if got := strings.Count(query, "(positionCaseInsensitive(block_events.domain, ?) > 0 OR positionCaseInsensitive(block_events.target, ?) > 0)"); got != 1 {
		t.Fatalf("block domain filter appears %d times, want 1:\n%s", got, query)
	}
	if !strings.Contains(query, "FROM flows WHERE flows.blocked = 0 AND started_at >= ? AND started_at <= ?") {
		t.Fatalf("flow branch is missing its date window:\n%s", query)
	}
	if !strings.Contains(query, "FROM block_events WHERE block_events.at >= ? AND block_events.at <= ?") {
		t.Fatalf("block branch is missing its date window:\n%s", query)
	}

	if len(args) != 24 {
		t.Fatalf("args = %#v, want 12 arguments per branch", args)
	}
	if !reflect.DeepEqual(args[:12], args[12:]) {
		t.Fatalf("branch args differ:\nflows:  %#v\nblocks: %#v", args[:12], args[12:])
	}
}

func TestTrafficDateWindowFollowsDecisionBranch(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	allowed, _, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: to, Decision: "allowed"}, true, false)
	if err != nil {
		t.Fatalf("allowed buildTrafficQuery: %v", err)
	}
	if !strings.Contains(allowed, "started_at >= ?") || strings.Contains(allowed, "block_events.at") {
		t.Fatalf("allowed query must only window the flows branch:\n%s", allowed)
	}

	blocked, _, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: to, Decision: "blocked"}, false, true)
	if err != nil {
		t.Fatalf("blocked buildTrafficQuery: %v", err)
	}
	if !strings.Contains(blocked, "block_events.at >= ?") || strings.Contains(blocked, "started_at") {
		t.Fatalf("blocked query must only window the block branch:\n%s", blocked)
	}
}

func TestTrafficConditionsMatchAndNegate(t *testing.T) {
	all, args, err := trafficConditions(domain.TrafficQuery{
		Match: "all",
		Conditions: []domain.TrafficCondition{
			{Field: "node", Operator: "eq", Value: "node-1"},
			{Field: "protocol", Operator: "eq", Value: "tcp"},
		},
	})
	if err != nil {
		t.Fatalf("trafficConditions all: %v", err)
	}
	if want := "(node_id = ? AND protocol = ?)"; all != want {
		t.Fatalf("all conditions = %q, want %q", all, want)
	}
	if want := []any{"node-1", uint8(6)}; !reflect.DeepEqual(args, want) {
		t.Fatalf("all args = %#v, want %#v", args, want)
	}

	anyMode, _, err := trafficConditions(domain.TrafficQuery{
		Match: "any",
		Conditions: []domain.TrafficCondition{
			{Field: "node", Operator: "eq", Value: "node-1"},
			{Field: "protocol", Operator: "eq", Value: "tcp"},
		},
	})
	if err != nil {
		t.Fatalf("trafficConditions any: %v", err)
	}
	if want := "(node_id = ? OR protocol = ?)"; anyMode != want {
		t.Fatalf("any conditions = %q, want %q", anyMode, want)
	}

	// An empty match defaults to all.
	defaulted, _, err := trafficConditions(domain.TrafficQuery{
		Conditions: []domain.TrafficCondition{
			{Field: "node", Operator: "eq", Value: "node-1"},
			{Field: "protocol", Operator: "eq", Value: "tcp"},
		},
	})
	if err != nil {
		t.Fatalf("trafficConditions default: %v", err)
	}
	if defaulted != all {
		t.Fatalf("default match = %q, want AND form %q", defaulted, all)
	}

	negated, args, err := trafficConditions(domain.TrafficQuery{
		Conditions: []domain.TrafficCondition{{Field: "event.kind", Operator: "eq", Value: "block", Negate: true}},
	})
	if err != nil {
		t.Fatalf("trafficConditions negated kind: %v", err)
	}
	if want := "(NOT (kind = ?))"; negated != want {
		t.Fatalf("negated kind = %q, want %q", negated, want)
	}
	if want := []any{"block"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("negated kind args = %#v, want %#v", args, want)
	}

	negatedIP, args, err := trafficConditions(domain.TrafficQuery{
		Conditions: []domain.TrafficCondition{{Field: "ip.addr", Operator: "eq", Value: "8.8.8.8", Negate: true}},
	})
	if err != nil {
		t.Fatalf("trafficConditions negated ip: %v", err)
	}
	if want := "(NOT ((src_ip = toIPv6(?) OR dst_ip = toIPv6(?))))"; negatedIP != want {
		t.Fatalf("negated ip = %q, want %q", negatedIP, want)
	}
	if want := []any{"8.8.8.8", "8.8.8.8"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("negated ip args = %#v, want %#v", args, want)
	}

	// "is not" across the domain columns matches when none of domain, SNI or
	// target equal the value; negating it flips the conjunction into an OR.
	domainNe, args, err := trafficConditions(domain.TrafficQuery{
		Conditions: []domain.TrafficCondition{{Field: "domain", Operator: "ne", Value: "ads.example", Negate: true}},
	})
	if err != nil {
		t.Fatalf("trafficConditions negated domain: %v", err)
	}
	if want := "(NOT ((lower(domain) != lower(?) AND lower(sni) != lower(?) AND lower(target) != lower(?))))"; domainNe != want {
		t.Fatalf("negated domain = %q, want %q", domainNe, want)
	}
	if want := []any{"ads.example", "ads.example", "ads.example"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("negated domain args = %#v, want %#v", args, want)
	}

	// Any + negation keeps each condition's NOT group intact.
	anyNegated, _, err := trafficConditions(domain.TrafficQuery{
		Match: "any",
		Conditions: []domain.TrafficCondition{
			{Field: "event.kind", Operator: "eq", Value: "block", Negate: true},
			{Field: "node", Operator: "eq", Value: "node-1"},
		},
	})
	if err != nil {
		t.Fatalf("trafficConditions any negated: %v", err)
	}
	if want := "(NOT (kind = ?) OR node_id = ?)"; anyNegated != want {
		t.Fatalf("any negated = %q, want %q", anyNegated, want)
	}
}

// TestTrafficCountQuerySharesFilters proves the total is counted over the same
// union, branch selection and condition rows as the paginated data query.
func TestTrafficCountQuerySharesFilters(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	q := domain.TrafficQuery{
		From: from, To: to, NodeID: "node-1", ExcludeLocal: true, Match: "all",
		Conditions: []domain.TrafficCondition{
			{Field: "domain", Operator: "contains", Value: "example"},
			{Field: "event.kind", Operator: "eq", Value: "block"},
		},
	}

	_, dataArgs, err := buildTrafficQuery(q, true, true)
	if err != nil {
		t.Fatalf("buildTrafficQuery: %v", err)
	}
	count, countArgs, err := buildTrafficCountQuery(q, true, true)
	if err != nil {
		t.Fatalf("buildTrafficCountQuery: %v", err)
	}
	if !strings.HasPrefix(count, "SELECT count() FROM (") {
		t.Fatalf("count query = %q, want a count over the union", count)
	}
	if !strings.Contains(count, "FROM flows") || !strings.Contains(count, "FROM block_events") {
		t.Fatalf("count query is missing a union branch:\n%s", count)
	}
	if !strings.Contains(count, "started_at >= ?") || !strings.Contains(count, "block_events.at >= ?") {
		t.Fatalf("count query is missing the date window:\n%s", count)
	}
	if strings.Contains(count, "ORDER BY") || strings.Contains(count, "LIMIT") {
		t.Fatalf("count query must not paginate:\n%s", count)
	}
	if !reflect.DeepEqual(countArgs, dataArgs) {
		t.Fatalf("count args = %#v, want data args %#v", countArgs, dataArgs)
	}
	if got, want := strings.Count(count, "?"), len(countArgs); got != want {
		t.Fatalf("count placeholders = %d, args = %d", got, want)
	}

	blocked, blockedArgs, err := buildTrafficCountQuery(q, false, true)
	if err != nil {
		t.Fatalf("buildTrafficCountQuery blocked: %v", err)
	}
	if strings.Contains(blocked, "FROM flows") || !strings.Contains(blocked, "FROM block_events") {
		t.Fatalf("blocked count must only read block events:\n%s", blocked)
	}
	if len(blockedArgs) == 0 {
		t.Fatal("blocked count is missing its arguments")
	}
}

func TestTrafficConditionsAreParameterized(t *testing.T) {
	cases := []domain.TrafficCondition{
		{Field: "text", Operator: "contains", Value: "evil' OR 1=1 --"},
		{Field: "node", Operator: "eq", Value: "node'--"},
		{Field: "domain", Operator: "starts_with", Value: "' OR '"},
		{Field: "target", Operator: "not_contains", Value: "x') UNION ALL SELECT 1 --"},
		{Field: "ip.dst", Operator: "eq", Value: "2001:db8::1"},
	}
	for _, condition := range cases {
		query, args, err := trafficConditions(domain.TrafficQuery{
			Match: "all", Conditions: []domain.TrafficCondition{condition},
		})
		if err != nil {
			t.Fatalf("%s: trafficConditions: %v", condition.Field, err)
		}
		if strings.Contains(query, condition.Value) {
			t.Fatalf("%s: raw value %q was interpolated into the query: %s", condition.Field, condition.Value, query)
		}
		if got, want := strings.Count(query, "?"), len(args); got != want {
			t.Fatalf("%s: placeholders = %d, args = %d", condition.Field, got, want)
		}
	}
}

func TestTrafficConditionArgumentTypes(t *testing.T) {
	cases := []struct {
		name      string
		condition domain.TrafficCondition
		wantArgs  []any
	}{
		{"source port", domain.TrafficCondition{Field: "src.port", Operator: "eq", Value: "443"}, []any{uint16(443)}},
		{"destination port", domain.TrafficCondition{Field: "dst.port", Operator: "lte", Value: "53"}, []any{uint16(53)}},
		{"either port", domain.TrafficCondition{Field: "port", Operator: "ne", Value: "8080"}, []any{uint16(8080), uint16(8080)}},
		{"protocol name", domain.TrafficCondition{Field: "protocol", Operator: "eq", Value: "tcp"}, []any{uint8(6)}},
		{"protocol number", domain.TrafficCondition{Field: "protocol", Operator: "eq", Value: "58"}, []any{uint8(58)}},
		{"duration", domain.TrafficCondition{Field: "duration", Operator: "gte", Value: "1500"}, []any{uint32(1500)}},
		{"ingress bytes", domain.TrafficCondition{Field: "bytes.in", Operator: "lt", Value: "7"}, []any{uint64(7)}},
		{"decision", domain.TrafficCondition{Field: "decision", Operator: "eq", Value: "blocked"}, []any{true}},
		{"source address", domain.TrafficCondition{Field: "ip.src", Operator: "eq", Value: "::ffff:8.8.8.8"}, []any{"8.8.8.8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, args, err := trafficCondition(tc.condition)
			if err != nil {
				t.Fatalf("trafficCondition: %v", err)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, tc.wantArgs)
			}
		})
	}
}

func TestTrafficConditionsValidation(t *testing.T) {
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	if _, _, err := trafficConditions(domain.TrafficQuery{
		Match: "some", Conditions: []domain.TrafficCondition{{Field: "node", Operator: "eq", Value: "node-1"}},
	}); err == nil {
		t.Fatal("expected an error for an invalid match mode")
	}

	many := make([]domain.TrafficCondition, 51)
	for i := range many {
		many[i] = domain.TrafficCondition{Field: "node", Operator: "eq", Value: "node-1"}
	}
	if _, _, err := buildTrafficQuery(domain.TrafficQuery{From: from, To: to, Conditions: many}, true, true); err == nil {
		t.Fatal("expected an error for more than 50 conditions")
	}

	for _, bad := range []domain.TrafficCondition{
		{Field: "", Operator: "eq", Value: "x"},
		{Field: "ip.src", Operator: "contains", Value: "8.8.8.8"},
		{Field: "src.port", Operator: "gt", Value: "70000"},
		{Field: "node", Operator: "contains", Value: "node-1"},
		{Field: "block.kind", Operator: "eq", Value: "other"},
	} {
		if _, _, err := trafficConditions(domain.TrafficQuery{Conditions: []domain.TrafficCondition{bad}}); err == nil {
			t.Fatalf("expected an error for %+v", bad)
		}
	}
}
