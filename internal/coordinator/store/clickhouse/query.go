package clickhouse

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
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
// localTrafficPredicate matches private, loopback, link-local, CGNAT,
// multicast and unspecified addresses in either address family. Traffic is
// considered local when both endpoints match.
func localTrafficPredicate(column string) string {
	prefixes := []string{
		"::ffff:0.0.0.0/104",
		"::ffff:10.0.0.0/104",
		"::ffff:100.64.0.0/106",
		"::ffff:127.0.0.0/104",
		"::ffff:169.254.0.0/112",
		"::ffff:172.16.0.0/108",
		"::ffff:192.168.0.0/112",
		"::ffff:224.0.0.0/100",
		"::ffff:240.0.0.0/100",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
		"ff00::/8",
		"::/128",
	}
	parts := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		parts = append(parts, fmt.Sprintf("isIPAddressInRange(%s, '%s')", column, prefix))
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

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
	if q.ExcludeLocal {
		w.add("NOT (" + localTrafficPredicate("src_ip") + " AND " + localTrafficPredicate("dst_ip") + ")")
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

func trafficConditions(q domain.TrafficQuery) (string, []any, error) {
	if len(q.Conditions) == 0 {
		return "", nil, nil
	}
	if len(q.Conditions) > 50 {
		return "", nil, apperr.Validation("at most 50 filter conditions are allowed")
	}
	match := strings.ToLower(strings.TrimSpace(q.Match))
	if match == "" {
		match = "all"
	}
	if match != "all" && match != "any" {
		return "", nil, apperr.Validation("match must be all or any")
	}
	clauses := make([]string, 0, len(q.Conditions))
	args := make([]any, 0, len(q.Conditions))
	for index, condition := range q.Conditions {
		expr, conditionArgs, err := trafficCondition(condition)
		if err != nil {
			return "", nil, apperr.Validation(fmt.Sprintf("condition %d: %v", index+1, err))
		}
		if condition.Negate {
			expr = "NOT (" + expr + ")"
		}
		clauses = append(clauses, expr)
		args = append(args, conditionArgs...)
	}
	joiner := " AND "
	if match == "any" {
		joiner = " OR "
	}
	return "(" + strings.Join(clauses, joiner) + ")", args, nil
}

func trafficCondition(condition domain.TrafficCondition) (string, []any, error) {
	field := strings.ToLower(strings.TrimSpace(condition.Field))
	operator := strings.ToLower(strings.TrimSpace(condition.Operator))
	value := strings.TrimSpace(condition.Value)
	if field == "" || operator == "" || value == "" {
		return "", nil, fmt.Errorf("field, operator and value are required")
	}

	switch field {
	case "event.kind":
		kind := strings.ToLower(value)
		if kind != "connection" && kind != "block" {
			return "", nil, fmt.Errorf("event.kind must be connection or block")
		}
		return comparison("kind", operator, kind)
	case "decision":
		decision := strings.ToLower(value)
		if decision != "allowed" && decision != "blocked" {
			return "", nil, fmt.Errorf("decision must be allowed or blocked")
		}
		return comparison("blocked", operator, decision == "blocked")
	case "ip.src", "src.ip":
		return ipCondition("src_ip", operator, value)
	case "ip.dst", "dst.ip":
		return ipCondition("dst_ip", operator, value)
	case "ip.addr":
		ip, err := validIP(value)
		if err != nil {
			return "", nil, err
		}
		switch operator {
		case "eq":
			return "(src_ip = toIPv6(?) OR dst_ip = toIPv6(?))", []any{ip, ip}, nil
		case "ne":
			return "(src_ip != toIPv6(?) AND dst_ip != toIPv6(?))", []any{ip, ip}, nil
		default:
			return "", nil, fmt.Errorf("ip.addr supports is and is not")
		}
	case "src.port":
		return numericCondition("src_port", operator, value, 16)
	case "dst.port":
		return numericCondition("dst_port", operator, value, 16)
	case "port":
		return numericEither([]string{"src_port", "dst_port"}, operator, value, 16)
	case "protocol":
		protocol, err := protocolNumber(value)
		if err != nil {
			return "", nil, err
		}
		return comparison("protocol", operator, protocol)
	case "text":
		return stringCondition([]string{"domain", "sni", "target", "reason", "container_name", "container_id"}, operator, value)
	case "domain":
		return stringCondition([]string{"domain", "sni", "target"}, operator, value)
	case "sni":
		return stringCondition([]string{"sni"}, operator, value)
	case "target":
		return stringCondition([]string{"target"}, operator, value)
	case "block.kind":
		kind := strings.ToLower(value)
		switch kind {
		case "ip", "cidr", "domain", "asn", "dns":
		default:
			return "", nil, fmt.Errorf("block.kind must be ip, cidr, domain, asn or dns")
		}
		return comparison("block_kind", operator, kind)
	case "node":
		if operator != "eq" && operator != "ne" {
			return "", nil, fmt.Errorf("node supports is and is not")
		}
		return comparison("node_id", operator, value)
	case "container":
		return stringCondition([]string{"container_name", "container_id"}, operator, value)
	case "bytes.out":
		return numericCondition("bytes_out", operator, value, 64)
	case "bytes.in":
		return numericCondition("bytes_in", operator, value, 64)
	case "duration":
		return numericCondition("duration_ms", operator, value, 32)
	case "reason":
		return stringCondition([]string{"reason"}, operator, value)
	default:
		return "", nil, fmt.Errorf("unsupported field %q", condition.Field)
	}
}

func comparison(column, operator string, value any) (string, []any, error) {
	sqlOperator, ok := map[string]string{
		"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	}[operator]
	if !ok {
		return "", nil, fmt.Errorf("operator %q is not supported for this field", operator)
	}
	return fmt.Sprintf("%s %s ?", column, sqlOperator), []any{value}, nil
}

func ipCondition(column, operator, value string) (string, []any, error) {
	ip, err := validIP(value)
	if err != nil {
		return "", nil, err
	}
	switch operator {
	case "eq":
		return fmt.Sprintf("%s = toIPv6(?)", column), []any{ip}, nil
	case "ne":
		return fmt.Sprintf("%s != toIPv6(?)", column), []any{ip}, nil
	default:
		return "", nil, fmt.Errorf("%s supports is and is not", column)
	}
}

func protocolNumber(value string) (uint8, error) {
	switch strings.ToLower(value) {
	case "icmp":
		return 1, nil
	case "tcp":
		return 6, nil
	case "udp":
		return 17, nil
	case "icmpv6", "icmp6":
		return 58, nil
	}
	number, err := strconv.ParseUint(value, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("protocol must be tcp, udp, icmp, icmpv6 or a number")
	}
	return uint8(number), nil
}

func numericCondition(column, operator, value string, bits int) (string, []any, error) {
	sqlOperator, ok := map[string]string{
		"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	}[operator]
	if !ok {
		return "", nil, fmt.Errorf("operator %q is not supported for this field", operator)
	}
	number, err := strconv.ParseUint(value, 10, bits)
	if err != nil {
		return "", nil, fmt.Errorf("value must be a non-negative integer")
	}
	var argument any
	switch bits {
	case 16:
		argument = uint16(number)
	case 32:
		argument = uint32(number)
	default:
		argument = number
	}
	return fmt.Sprintf("%s %s ?", column, sqlOperator), []any{argument}, nil
}

func numericEither(columns []string, operator, value string, bits int) (string, []any, error) {
	sqlOperator, ok := map[string]string{
		"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	}[operator]
	if !ok {
		return "", nil, fmt.Errorf("operator %q is not supported for this field", operator)
	}
	number, err := strconv.ParseUint(value, 10, bits)
	if err != nil {
		return "", nil, fmt.Errorf("value must be a non-negative integer")
	}
	var argument any
	switch bits {
	case 16:
		argument = uint16(number)
	case 32:
		argument = uint32(number)
	default:
		argument = number
	}
	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		clauses = append(clauses, fmt.Sprintf("%s %s ?", column, sqlOperator))
		args = append(args, argument)
	}
	joiner := " OR "
	if operator == "ne" {
		joiner = " AND "
	}
	return "(" + strings.Join(clauses, joiner) + ")", args, nil
}

func stringCondition(columns []string, operator, value string) (string, []any, error) {
	orAll := true
	switch operator {
	case "eq":
		orAll = true
	case "ne":
		orAll = false
	case "contains":
		orAll = true
	case "not_contains":
		orAll = false
	case "starts_with", "ends_with":
		orAll = true
	default:
		return "", nil, fmt.Errorf("operator %q is not supported for this field", operator)
	}
	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		switch operator {
		case "eq":
			clauses = append(clauses, fmt.Sprintf("lower(%s) = lower(?)", column))
		case "ne":
			clauses = append(clauses, fmt.Sprintf("lower(%s) != lower(?)", column))
		case "contains":
			clauses = append(clauses, fmt.Sprintf("positionCaseInsensitive(%s, ?) > 0", column))
		case "not_contains":
			clauses = append(clauses, fmt.Sprintf("positionCaseInsensitive(%s, ?) = 0", column))
		case "starts_with":
			clauses = append(clauses, fmt.Sprintf("startsWith(lower(%s), lower(?))", column))
		case "ends_with":
			clauses = append(clauses, fmt.Sprintf("endsWith(lower(%s), lower(?))", column))
		}
		args = append(args, value)
	}
	joiner := " OR "
	if !orAll {
		joiner = " AND "
	}
	return "(" + strings.Join(clauses, joiner) + ")", args, nil
}

func buildTrafficUnion(q domain.TrafficQuery, includeFlows, includeBlocks bool) (string, []any, error) {
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
	return strings.Join(union, "\nUNION ALL\n"), args, nil
}

// buildTrafficQuery constructs the paginated union over the selected branches.
// Simple filters are applied inside each branch; condition rows are applied to
// the union output so fields such as kind and target behave uniformly.
func buildTrafficQuery(q domain.TrafficQuery, includeFlows, includeBlocks bool) (string, []any, error) {
	union, args, err := buildTrafficUnion(q, includeFlows, includeBlocks)
	if err != nil {
		return "", nil, err
	}
	conditions, conditionArgs, err := trafficConditions(q)
	if err != nil {
		return "", nil, err
	}
	query := "SELECT * FROM (" + union + ")"
	if conditions != "" {
		query += " WHERE " + conditions
		args = append(args, conditionArgs...)
	}
	limit, offset := paginate(q.Limit, q.Offset)
	query += fmt.Sprintf(" ORDER BY at DESC, event_id DESC LIMIT %d OFFSET %d", limit, offset)
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
	if len(q.Conditions) > 0 {
		union, unionArgs, err := buildTrafficUnion(q, includeFlows, includeBlocks)
		if err != nil {
			return nil, 0, err
		}
		conditions, conditionArgs, err := trafficConditions(q)
		if err != nil {
			return nil, 0, err
		}
		countQuery := "SELECT count() FROM (" + union + ")"
		countArgs := append([]any{}, unionArgs...)
		if conditions != "" {
			countQuery += " WHERE " + conditions
			countArgs = append(countArgs, conditionArgs...)
		}
		if err := s.conn.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
			return nil, 0, apperr.Internal("count traffic events", err)
		}
	} else {
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
