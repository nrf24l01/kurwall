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
		"packets_in", "packets_out", "blocked", "rule_id", "reason",
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
