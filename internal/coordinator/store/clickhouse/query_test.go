package clickhouse

import "testing"

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
