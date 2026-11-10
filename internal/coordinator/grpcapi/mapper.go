package grpcapi

import (
	"time"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

// SnapshotToProto converts a domain snapshot into its wire representation.
func SnapshotToProto(snap domain.Snapshot) *kurwallv1.PolicySnapshot {
	out := &kurwallv1.PolicySnapshot{
		Version:             snap.Version,
		NodeId:              snap.NodeID,
		GeneratedAtUnixNano: snap.GeneratedAt.UnixNano(),
		Rules:               make([]*kurwallv1.PolicyRule, 0, len(snap.Rules)),
		Settings: &kurwallv1.AgentSettings{
			TlsPorts:        snap.Settings.TLSInspectorPorts,
			DnsInspection:   snap.Settings.DNSInspection,
			TlsInspection:   snap.Settings.TLSInspection,
			BatchMaxEvents:  snap.Settings.BatchMaxEvents,
			FlushIntervalMs: snap.Settings.FlushIntervalMS,
		},
		Policies: make([]*kurwallv1.PolicyMeta, 0, len(snap.Policies)),
	}
	for _, rule := range snap.Rules {
		out.Rules = append(out.Rules, &kurwallv1.PolicyRule{
			Id:         rule.ID,
			PolicyId:   rule.PolicyID,
			PolicyName: rule.PolicyName,
			Kind:       kindToProto(rule.Kind),
			Value:      rule.Value,
			Action:     actionToProto(rule.Action),
			Note:       rule.Note,
			Prefixes:   rule.Prefixes,
		})
	}
	for _, meta := range snap.Policies {
		out.Policies = append(out.Policies, &kurwallv1.PolicyMeta{Id: meta.ID, Name: meta.Name})
	}
	if snap.GeneratedAt.IsZero() {
		out.GeneratedAtUnixNano = time.Now().UnixNano()
	}
	return out
}

func kindToProto(kind string) kurwallv1.RuleKind {
	switch kind {
	case domain.RuleKindIP:
		return kurwallv1.RuleKind_RULE_KIND_IP
	case domain.RuleKindCIDR:
		return kurwallv1.RuleKind_RULE_KIND_CIDR
	case domain.RuleKindDomain:
		return kurwallv1.RuleKind_RULE_KIND_DOMAIN
	case domain.RuleKindASN:
		return kurwallv1.RuleKind_RULE_KIND_ASN
	default:
		return kurwallv1.RuleKind_RULE_KIND_UNSPECIFIED
	}
}

func actionToProto(action string) kurwallv1.RuleAction {
	if action == domain.RuleActionBlock {
		return kurwallv1.RuleAction_RULE_ACTION_BLOCK
	}
	return kurwallv1.RuleAction_RULE_ACTION_UNSPECIFIED
}
