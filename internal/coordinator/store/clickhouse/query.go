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

// trafficFlowColumns and trafficBlockColumns are the 25 columns of the two
// union branches, in the shared output order scanned by Store.Traffic.
var trafficFlowColumns = []string{
	"event_id",
	"'connection' AS kind",
	"node_id",
	"started_at AS at",
	"ended_at",
	"duration_ms",
	"src_ip",
	"dst_ip",
	"src_port",
	"dst_port",
	"protocol",
	"domain",
	"sni",
	"'' AS target",
	"'' AS block_kind",
	"container_id",
	"container_name",
	"bytes_in",
	"bytes_out",
	"toUInt64(packets_in) AS packets_in",
	"toUInt64(packets_out) AS packets_out",
	"false AS blocked",
	"rule_id",
	"reason",
	"network",
}

var trafficBlockColumns = []string{
	"event_id",
	"'block' AS kind",
	"node_id",
	"block_events.at AS at",
	"block_events.at AS ended_at",
	"toUInt32(0) AS duration_ms",
	"src_ip",
	"dst_ip",
	"src_port",
	"dst_port",
	"protocol",
	"domain",
	"'' AS sni",
	"target",
	"block_events.kind AS block_kind",
	"container_id",
	"container_name",
	"toUInt64(0) AS bytes_in",
	"toUInt64(0) AS bytes_out",
	"toUInt64(0) AS packets_in",
	"toUInt64(0) AS packets_out",
	"true AS blocked",
	"rule_id",
	"reason",
	"'' AS network",
}

// trafficBranches reports which storage tables a decision filter includes.
func trafficBranches(decision string) (flows, blocks bool) {
	switch decision {
	case "allowed":
		return true, false
	case "blocked":
		return false, true
	default:
		return true, true
	}
}

// trafficWhere builds the filters shared by the traffic counts and branches.
func trafficWhere(q domain.TrafficQuery, flows bool) (*where, error) {
	w := &where{}
	if flows {
		w.add("flows.blocked = 0")
		w.add("started_at >= ?", q.From)
		w.add("started_at <= ?", q.To)
	} else {
		w.add("block_events.at >= ?", q.From)
		w.add("block_events.at <= ?", q.To)
	}
	if q.NodeID != "" {
		w.add("node_id = ?", q.NodeID)
	}
	if q.Protocol != nil {
		w.add("protocol = ?", *q.Protocol)
	}
	if q.Container != "" {
		w.add("(positionCaseInsensitive(container_name, ?) > 0 OR positionCaseInsensitive(container_id, ?) > 0)", q.Container, q.Container)
	}
	if q.SrcIP != "" {
		ip, err := validIP(q.SrcIP)
		if err != nil {
			return nil, err
		}
		w.add("src_ip = toIPv6(?)", ip)
	}
	if q.DstIP != "" {
		ip, err := validIP(q.DstIP)
		if err != nil {
			return nil, err
		}
		w.add("dst_ip = toIPv6(?)", ip)
	}
	if q.IP != "" {
		ip, err := validIP(q.IP)
		if err != nil {
			return nil, err
		}
		w.add("(src_ip = toIPv6(?) OR dst_ip = toIPv6(?))", ip, ip)
	}
	if q.Domain != "" {
		if flows {
			w.add("(positionCaseInsensitive(domain, ?) > 0 OR positionCaseInsensitive(sni, ?) > 0)", q.Domain, q.Domain)
		} else {
			w.add("(positionCaseInsensitive(block_events.domain, ?) > 0 OR positionCaseInsensitive(block_events.target, ?) > 0)", q.Domain, q.Domain)
		}
	}
	return w, nil
}

// buildTrafficQuery constructs the paginated union over the selected branches.
// Filters are applied inside each branch before the union.
func buildTrafficQuery(q domain.TrafficQuery, includeFlows, includeBlocks bool) (string, []any, error) {
	flowWhere, err := trafficWhere(q, true)
	if err != nil {
		return "", nil, err
	}
	blockWhere, err := trafficWhere(q, false)
	if err != nil {
		return "", nil, err
	}

	union := make([]string, 0, 2)
	args := make([]any, 0, len(flowWhere.args)+len(blockWhere.args))
	if includeFlows {
		union = append(union, "SELECT "+strings.Join(trafficFlowColumns, ",\n\t\t\t")+"\n\t\t\tFROM flows"+flowWhere.sql())
		args = append(args, flowWhere.args...)
	}
	if includeBlocks {
		union = append(union, "SELECT "+strings.Join(trafficBlockColumns, ",\n\t\t\t")+"\n\t\t\tFROM block_events"+blockWhere.sql())
		args = append(args, blockWhere.args...)
	}

	limit, offset := paginate(q.Limit, q.Offset)
	query := "SELECT * FROM (" + strings.Join(union, "\nUNION ALL\n") + ")" +
		fmt.Sprintf(" ORDER BY at DESC, event_id DESC LIMIT %d OFFSET %d", limit, offset)
	return query, args, nil
}

// Traffic returns allowed connections and blocking decisions in one feed.
func (s *Store) Traffic(ctx context.Context, q domain.TrafficQuery) ([]domain.TrafficEvent, uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	includeFlows, includeBlocks := trafficBranches(q.Decision)
	flowWhere, err := trafficWhere(q, true)
	if err != nil {
		return nil, 0, err
	}
	blockWhere, err := trafficWhere(q, false)
	if err != nil {
		return nil, 0, err
	}

	var total uint64
	if includeFlows {
		if err := s.conn.QueryRow(ctx, "SELECT count() FROM flows"+flowWhere.sql(), flowWhere.args...).Scan(&total); err != nil {
			return nil, 0, apperr.Internal("count traffic flows", err)
		}
	}
	if includeBlocks {
		var blocks uint64
		if err := s.conn.QueryRow(ctx, "SELECT count() FROM block_events"+blockWhere.sql(), blockWhere.args...).Scan(&blocks); err != nil {
			return nil, 0, apperr.Internal("count traffic blocks", err)
		}
		total += blocks
	}

	limit, _ := paginate(q.Limit, q.Offset)
	query, args, err := buildTrafficQuery(q, includeFlows, includeBlocks)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, apperr.Internal("query traffic events", err)
	}
	defer rows.Close()

	out := make([]domain.TrafficEvent, 0, limit)
	for rows.Next() {
		var e domain.TrafficEvent
		var src, dst net.IP
		if err := rows.Scan(&e.EventID, &e.Kind, &e.NodeID, &e.At, &e.EndedAt, &e.DurationMS,
			&src, &dst, &e.SrcPort, &e.DstPort, &e.Protocol, &e.Domain, &e.SNI,
			&e.Target, &e.BlockKind, &e.ContainerID, &e.ContainerName,
			&e.BytesIn, &e.BytesOut, &e.PacketsIn, &e.PacketsOut,
			&e.Blocked, &e.RuleID, &e.Reason, &e.Network); err != nil {
			return nil, 0, apperr.Internal("scan traffic event", err)
		}
		e.SrcIP = formatRawIP(src)
		e.DstIP = formatRawIP(dst)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func formatRawIP(ip net.IP) string {
	return FormatIP(ip)
}
