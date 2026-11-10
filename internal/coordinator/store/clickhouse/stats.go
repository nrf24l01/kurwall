package clickhouse

import (
	"context"
	"net"
	"sort"
	"time"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// Stats aggregates dashboard counters for a time window.
func (s *Store) Stats(ctx context.Context, from, to time.Time) (domain.Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	stats := domain.Stats{Window: domain.StatsWindow{From: from, To: to}}
	if err := s.conn.QueryRow(ctx, `SELECT count(), countIf(blocked), sum(bytes_in), sum(bytes_out)
		FROM flows WHERE started_at >= ? AND started_at <= ?`, from, to).
		Scan(&stats.Flows, &stats.BlockedFlows, &stats.BytesIn, &stats.BytesOut); err != nil {
		return stats, apperr.Internal("aggregate flows", err)
	}
	if err := s.conn.QueryRow(ctx, `SELECT count(), countIf(blocked) FROM dns_events WHERE at >= ? AND at <= ?`, from, to).
		Scan(&stats.DNSQueries, &stats.BlockedDNS); err != nil {
		return stats, apperr.Internal("aggregate dns", err)
	}
	var connectionBlocks uint64
	if err := s.conn.QueryRow(ctx, `SELECT count(), countIf(kind IN ('ip', 'cidr', 'domain', 'asn'))
		FROM block_events WHERE at >= ? AND at <= ?`, from, to).
		Scan(&stats.BlockEvents, &connectionBlocks); err != nil {
		return stats, apperr.Internal("aggregate blocks", err)
	}
	// DNS blocks are not connections; IP/CIDR/SNI blocks are failed connection
	// attempts and belong in the blocked-connection counter. Blocking events are
	// emitted immediately, while a blocked flow record may arrive later, so the
	// event count is authoritative and avoids double counting.
	stats.BlockedFlows = connectionBlocks

	stats.TopDomains = s.topStrings(ctx, `SELECT if(domain != '', domain, sni) AS name, count() AS c
		FROM flows WHERE started_at >= ? AND started_at <= ? AND name != ''
		GROUP BY name ORDER BY c DESC LIMIT 10`, from, to)
	stats.TopContainers = s.topStrings(ctx, `SELECT container_name AS name, count() AS c
		FROM flows WHERE started_at >= ? AND started_at <= ? AND name != ''
		GROUP BY name ORDER BY c DESC LIMIT 10`, from, to)

	rows, err := s.conn.Query(ctx, `SELECT dst_ip, count() AS c FROM flows
		WHERE started_at >= ? AND started_at <= ? GROUP BY dst_ip ORDER BY c DESC LIMIT 10`, from, to)
	if err != nil {
		return stats, apperr.Internal("top destinations", err)
	}
	defer rows.Close()
	stats.TopDestinations = []domain.CountItem{}
	for rows.Next() {
		var ip net.IP
		var count uint64
		if err := rows.Scan(&ip, &count); err != nil {
			return stats, apperr.Internal("scan top destination", err)
		}
		stats.TopDestinations = append(stats.TopDestinations, domain.CountItem{Name: FormatIP(ip), Count: count})
	}

	stats.Nodes = s.nodeActivity(ctx, from, to)
	return stats, nil
}

func (s *Store) topStrings(ctx context.Context, query string, from, to time.Time) []domain.CountItem {
	items := []domain.CountItem{}
	rows, err := s.conn.Query(ctx, query, from, to)
	if err != nil {
		return items
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.CountItem
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return items
		}
		items = append(items, item)
	}
	return items
}

func (s *Store) nodeActivity(ctx context.Context, from, to time.Time) []domain.NodeActivity {
	byNode := map[string]*domain.NodeActivity{}
	rows, err := s.conn.Query(ctx, `SELECT node_id, count(), countIf(blocked), sum(bytes_out), max(started_at)
		FROM flows WHERE started_at >= ? AND started_at <= ? GROUP BY node_id`, from, to)
	if err == nil {
		for rows.Next() {
			var a domain.NodeActivity
			if err := rows.Scan(&a.NodeID, &a.Flows, &a.BlockedFlows, &a.BytesOut, &a.LastSeenAt); err != nil {
				break
			}
			byNode[a.NodeID] = &a
		}
		rows.Close()
	}
	rows, err = s.conn.Query(ctx, `SELECT node_id, count(), max(at)
		FROM dns_events WHERE at >= ? AND at <= ? GROUP BY node_id`, from, to)
	if err == nil {
		for rows.Next() {
			var nodeID string
			var count uint64
			var last time.Time
			if err := rows.Scan(&nodeID, &count, &last); err != nil {
				break
			}
			a := byNode[nodeID]
			if a == nil {
				a = &domain.NodeActivity{NodeID: nodeID}
				byNode[nodeID] = a
			}
			a.DNSQueries = count
			if last.After(a.LastSeenAt) {
				a.LastSeenAt = last
			}
		}
		rows.Close()
	}
	out := make([]domain.NodeActivity, 0, len(byNode))
	for _, a := range byNode {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flows != out[j].Flows {
			return out[i].Flows > out[j].Flows
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}
