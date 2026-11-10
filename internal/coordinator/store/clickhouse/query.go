package clickhouse

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

type where struct {
	clauses []string
	args    []any
}

func (w *where) add(clause string, args ...any) {
	w.clauses = append(w.clauses, clause)
	w.args = append(w.args, args...)
}

func (w *where) sql() string {
	if len(w.clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.clauses, " AND ")
}

func validIP(raw string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "", apperr.Validation("IP filter must be a valid IPv4 or IPv6 address")
	}
	return addr.Unmap().String(), nil
}

func paginate(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Flows returns consolidated connection history.
func (s *Store) Flows(ctx context.Context, q domain.FlowQuery) ([]domain.FlowRecord, uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	w := &where{}
	w.add("started_at >= ?", q.From)
	w.add("started_at <= ?", q.To)
	if q.NodeID != "" {
		w.add("node_id = ?", q.NodeID)
	}
	if q.SrcIP != "" {
		ip, err := validIP(q.SrcIP)
		if err != nil {
			return nil, 0, err
		}
		w.add("src_ip = toIPv6(?)", ip)
	}
	if q.DstIP != "" {
		ip, err := validIP(q.DstIP)
		if err != nil {
			return nil, 0, err
		}
		w.add("dst_ip = toIPv6(?)", ip)
	}
	if q.Domain != "" {
		w.add("(positionCaseInsensitive(domain, ?) > 0 OR positionCaseInsensitive(sni, ?) > 0)", q.Domain, q.Domain)
	}
	if q.Container != "" {
		w.add("(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)", q.Container, q.Container)
	}
	switch q.Decision {
	case "blocked":
		w.add("blocked = 1")
	case "allowed":
		w.add("blocked = 0")
	}
	if q.Protocol != nil {
		w.add("protocol = ?", *q.Protocol)
	}

	var total uint64
	if err := s.conn.QueryRow(ctx, "SELECT count() FROM flows"+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, apperr.Internal("count flows", err)
	}

	limit, offset := paginate(q.Limit, q.Offset)
	query := `SELECT node_id, event_id, started_at, ended_at, duration_ms, src_ip, dst_ip,
		src_port, dst_port, protocol, domain, sni, container_id, container_name,
		bytes_in, bytes_out, packets_in, packets_out, blocked, rule_id, reason, network
		FROM flows` + w.sql() + fmt.Sprintf(" ORDER BY started_at DESC LIMIT %d OFFSET %d", limit, offset)
	rows, err := s.conn.Query(ctx, query, w.args...)
	if err != nil {
		return nil, 0, apperr.Internal("query flows", err)
	}
	defer rows.Close()

	out := make([]domain.FlowRecord, 0, limit)
	for rows.Next() {
		var f domain.FlowRecord
		var src, dst net.IP
		var packetsIn, packetsOut uint32
		if err := rows.Scan(&f.NodeID, &f.EventID, &f.StartedAt, &f.EndedAt, &f.DurationMS,
			&src, &dst, &f.SrcPort, &f.DstPort, &f.Protocol, &f.Domain, &f.SNI,
			&f.ContainerID, &f.ContainerName, &f.BytesIn, &f.BytesOut,
			&packetsIn, &packetsOut, &f.Blocked, &f.RuleID, &f.Reason, &f.Network); err != nil {
			return nil, 0, apperr.Internal("scan flow", err)
		}
		f.PacketsIn = uint64(packetsIn)
		f.PacketsOut = uint64(packetsOut)
		f.SrcIP = formatRawIP(src)
		f.DstIP = formatRawIP(dst)
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// DNS returns DNS request history.
func (s *Store) DNS(ctx context.Context, q domain.DNSQuery) ([]domain.DNSRecord, uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	w := &where{}
	w.add("at >= ?", q.From)
	w.add("at <= ?", q.To)
	if q.NodeID != "" {
		w.add("node_id = ?", q.NodeID)
	}
	if q.ClientIP != "" {
		ip, err := validIP(q.ClientIP)
		if err != nil {
			return nil, 0, err
		}
		w.add("client_ip = toIPv6(?)", ip)
	}
	if q.Domain != "" {
		w.add("positionCaseInsensitive(qname, ?) > 0", q.Domain)
	}
	if q.Container != "" {
		w.add("(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)", q.Container, q.Container)
	}
	if q.Blocked != nil {
		if *q.Blocked {
			w.add("blocked = 1")
		} else {
			w.add("blocked = 0")
		}
	}

	var total uint64
	if err := s.conn.QueryRow(ctx, "SELECT count() FROM dns_events"+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, apperr.Internal("count dns events", err)
	}

	limit, offset := paginate(q.Limit, q.Offset)
	query := `SELECT node_id, event_id, at, client_ip, client_port, qname, qtype, rcode,
		answers, blocked, rule_id, container_id, container_name, latency_ms
		FROM dns_events` + w.sql() + fmt.Sprintf(" ORDER BY at DESC LIMIT %d OFFSET %d", limit, offset)
	rows, err := s.conn.Query(ctx, query, w.args...)
	if err != nil {
		return nil, 0, apperr.Internal("query dns events", err)
	}
	defer rows.Close()

	out := make([]domain.DNSRecord, 0, limit)
	for rows.Next() {
		var d domain.DNSRecord
		var client net.IP
		var answers []net.IP
		var qtype uint16
		var rcode uint8
		if err := rows.Scan(&d.NodeID, &d.EventID, &d.At, &client, &d.ClientPort, &d.QName,
			&qtype, &rcode, &answers, &d.Blocked, &d.RuleID, &d.ContainerID,
			&d.ContainerName, &d.LatencyMS); err != nil {
			return nil, 0, apperr.Internal("scan dns event", err)
		}
		d.QType = uint32(qtype)
		d.RCode = uint32(rcode)
		d.ClientIP = formatRawIP(client)
		d.Answers = make([]string, 0, len(answers))
		for _, a := range answers {
			d.Answers = append(d.Answers, FormatIP(a))
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// Blocks returns blocking event history.
func (s *Store) Blocks(ctx context.Context, q domain.BlockQuery) ([]domain.BlockRecord, uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	w := &where{}
	w.add("at >= ?", q.From)
	w.add("at <= ?", q.To)
	if q.NodeID != "" {
		w.add("node_id = ?", q.NodeID)
	}
	if q.Kind != "" {
		w.add("kind = ?", q.Kind)
	}
	if q.Target != "" {
		w.add("positionCaseInsensitive(target, ?) > 0", q.Target)
	}
	if q.Domain != "" {
		w.add("positionCaseInsensitive(domain, ?) > 0", q.Domain)
	}
	if q.Container != "" {
		w.add("(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)", q.Container, q.Container)
	}
	if q.SrcIP != "" {
		ip, err := validIP(q.SrcIP)
		if err != nil {
			return nil, 0, err
		}
		w.add("src_ip = toIPv6(?)", ip)
	}
	if q.DstIP != "" {
		ip, err := validIP(q.DstIP)
		if err != nil {
			return nil, 0, err
		}
		w.add("dst_ip = toIPv6(?)", ip)
	}

	var total uint64
	if err := s.conn.QueryRow(ctx, "SELECT count() FROM block_events"+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, apperr.Internal("count block events", err)
	}

	limit, offset := paginate(q.Limit, q.Offset)
	query := `SELECT node_id, event_id, at, kind, target, src_ip, dst_ip, src_port, dst_port,
		protocol, domain, container_id, container_name, rule_id, reason
		FROM block_events` + w.sql() + fmt.Sprintf(" ORDER BY at DESC LIMIT %d OFFSET %d", limit, offset)
	rows, err := s.conn.Query(ctx, query, w.args...)
	if err != nil {
		return nil, 0, apperr.Internal("query block events", err)
	}
	defer rows.Close()

	out := make([]domain.BlockRecord, 0, limit)
	for rows.Next() {
		var b domain.BlockRecord
		var src, dst net.IP
		if err := rows.Scan(&b.NodeID, &b.EventID, &b.At, &b.Kind, &b.Target, &src, &dst,
			&b.SrcPort, &b.DstPort, &b.Protocol, &b.Domain, &b.ContainerID,
			&b.ContainerName, &b.RuleID, &b.Reason); err != nil {
			return nil, 0, apperr.Internal("scan block event", err)
		}
		b.SrcIP = formatRawIP(src)
		b.DstIP = formatRawIP(dst)
		out = append(out, b)
	}
	return out, total, rows.Err()
}

func formatRawIP(ip net.IP) string {
	return FormatIP(ip)
}
