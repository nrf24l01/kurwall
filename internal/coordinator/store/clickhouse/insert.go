package clickhouse

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// InsertBatch writes a mixed event batch into the three event tables.
func (s *Store) InsertBatch(ctx context.Context, nodeID string, events []domain.Event) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var flowCount, dnsCount, blockCount int
	for _, ev := range events {
		switch ev.Kind {
		case "flow":
			flowCount++
		case "dns":
			dnsCount++
		case "block":
			blockCount++
		}
	}

	if flowCount > 0 {
		batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO flows (
			node_id, event_id, started_at, ended_at, duration_ms, src_ip, dst_ip, src_port, dst_port,
			protocol, domain, sni, container_id, container_name, bytes_in, bytes_out,
			packets_in, packets_out, blocked, rule_id, reason, network)`)
		if err != nil {
			return apperr.Internal("prepare flow insert", err)
		}
		for _, ev := range events {
			if ev.Kind != "flow" || ev.Flow == nil {
				continue
			}
			f := ev.Flow
			duration := f.DurationMS
			if duration == 0 && !f.EndedAt.IsZero() && !f.StartedAt.IsZero() {
				ms := f.EndedAt.Sub(f.StartedAt).Milliseconds()
				if ms > 0 && ms < 1<<32 {
					duration = uint32(ms)
				}
			}
			err := batch.Append(nodeID, f.EventID, f.StartedAt, f.EndedAt, duration,
				ipValue(f.SrcIP), ipValue(f.DstIP), f.SrcPort, f.DstPort, f.Protocol,
				f.Domain, f.SNI, f.ContainerID, f.ContainerName,
				f.BytesIn, f.BytesOut, uint32(f.PacketsIn), uint32(f.PacketsOut),
				f.Blocked, f.RuleID, f.Reason, f.Network)
			if err != nil {
				return apperr.Internal("append flow", err)
			}
		}
		if err := batch.Send(); err != nil {
			return apperr.Internal("insert flows", err)
		}
	}

	if dnsCount > 0 {
		batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO dns_events (
			node_id, event_id, at, client_ip, client_port, qname, qtype, rcode, answers,
			blocked, rule_id, container_id, container_name, latency_ms)`)
		if err != nil {
			return apperr.Internal("prepare dns insert", err)
		}
		for _, ev := range events {
			if ev.Kind != "dns" || ev.DNS == nil {
				continue
			}
			d := ev.DNS
			err := batch.Append(nodeID, d.EventID, d.At, ipValue(d.ClientIP), d.ClientPort,
				d.QName, uint16(d.QType), uint8(d.RCode), ipList(d.Answers),
				d.Blocked, d.RuleID, d.ContainerID, d.ContainerName, d.LatencyMS)
			if err != nil {
				return apperr.Internal("append dns event", err)
			}
		}
		if err := batch.Send(); err != nil {
			return apperr.Internal("insert dns events", err)
		}
	}

	if blockCount > 0 {
		batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO block_events (
			node_id, event_id, at, kind, target, src_ip, dst_ip, src_port, dst_port,
			protocol, domain, container_id, container_name, rule_id, reason)`)
		if err != nil {
			return apperr.Internal("prepare block insert", err)
		}
		for _, ev := range events {
			if ev.Kind != "block" || ev.Block == nil {
				continue
			}
			b := ev.Block
			err := batch.Append(nodeID, b.EventID, b.At, b.Kind, b.Target,
				ipValue(b.SrcIP), ipValue(b.DstIP), b.SrcPort, b.DstPort, b.Protocol,
				b.Domain, b.ContainerID, b.ContainerName, b.RuleID, b.Reason)
			if err != nil {
				return apperr.Internal("append block event", err)
			}
		}
		if err := batch.Send(); err != nil {
			return apperr.Internal("insert block events", err)
		}
	}
	return nil
}

func ipValue(raw string) net.IP {
	if raw == "" {
		return net.IPv6zero
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return net.IPv6zero
	}
	b := addr.Unmap().As16()
	return net.IP(b[:])
}

func ipList(raw []string) []net.IP {
	out := make([]net.IP, 0, len(raw))
	for _, s := range raw {
		ip := ipValue(s)
		if ip.Equal(net.IPv6zero) {
			continue
		}
		out = append(out, ip)
	}
	return out
}

// FormatIP renders ClickHouse IPv6 values back into their natural IPv4 form.
func FormatIP(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// ProtocolName maps IP protocol numbers to short labels.
func ProtocolName(proto uint8) string {
	switch proto {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 1:
		return "icmp"
	case 58:
		return "icmpv6"
	default:
		return fmt.Sprintf("%d", proto)
	}
}
