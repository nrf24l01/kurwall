package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/service"
)

// stubEventReader captures the query the handler builds without touching
// ClickHouse.
type stubEventReader struct {
	domain.EventReader
	traffic domain.TrafficQuery
	dns     domain.DNSQuery
	called  bool
}

func (s *stubEventReader) Flows(context.Context, domain.FlowQuery) ([]domain.FlowRecord, uint64, error) {
	return nil, 0, nil
}

func (s *stubEventReader) DNS(_ context.Context, q domain.DNSQuery) ([]domain.DNSRecord, uint64, error) {
	s.called = true
	s.dns = q
	return nil, 0, nil
}

func (s *stubEventReader) Blocks(context.Context, domain.BlockQuery) ([]domain.BlockRecord, uint64, error) {
	return nil, 0, nil
}

func (s *stubEventReader) Traffic(_ context.Context, q domain.TrafficQuery) ([]domain.TrafficEvent, uint64, error) {
	s.called = true
	s.traffic = q
	return nil, 0, nil
}

func (s *stubEventReader) Stats(context.Context, time.Time, time.Time) (domain.Stats, error) {
	return domain.Stats{}, nil
}

func (s *stubEventReader) Health(context.Context) error { return nil }

func newEventContext(t *testing.T, path string, params url.Values) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	return echo.New().NewContext(req, rec), rec
}

func TestListTrafficMapsEveryFilter(t *testing.T) {
	reader := &stubEventReader{}
	h := &Handler{Events: service.NewEvents(nil, nil, reader, nil, 0, 0)}
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	conditions := []domain.TrafficCondition{
		{Field: "ip.dst", Operator: "eq", Value: "8.8.8.8"},
		{Field: "domain", Operator: "not_contains", Value: "ads", Negate: true},
	}
	rawFilter, err := json.Marshal(conditions)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}

	c, rec := newEventContext(t, "/api/v1/events/traffic", url.Values{
		"from":         {from.Format(time.RFC3339)},
		"to":           {to.Format(time.RFC3339)},
		"nodeId":       {"node-1"},
		"srcIp":        {"10.0.0.1"},
		"dstIp":        {"8.8.8.8"},
		"ip":           {"1.1.1.1"},
		"domain":       {"example.com"},
		"container":    {"web"},
		"decision":     {"allowed"},
		"protocol":     {"6"},
		"excludeLocal": {"true"},
		"match":        {"any"},
		"filter":       {string(rawFilter)},
		"limit":        {"25"},
		"offset":       {"50"},
	})
	if err := h.ListTraffic(c); err != nil {
		t.Fatalf("ListTraffic: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	got := reader.traffic
	if !got.From.Equal(from) || !got.To.Equal(to) {
		t.Fatalf("window = (%v, %v), want (%v, %v)", got.From, got.To, from, to)
	}
	if got.NodeID != "node-1" || got.SrcIP != "10.0.0.1" || got.DstIP != "8.8.8.8" ||
		got.IP != "1.1.1.1" || got.Domain != "example.com" || got.Container != "web" ||
		got.Decision != "allowed" || got.Match != "any" ||
		got.Limit != 25 || got.Offset != 50 {
		t.Fatalf("query = %+v, want every request filter forwarded", got)
	}
	if !got.ExcludeLocal {
		t.Fatal("excludeLocal was not forwarded")
	}
	if got.Protocol == nil || *got.Protocol != 6 {
		t.Fatalf("protocol = %v, want 6", got.Protocol)
	}
	if !reflect.DeepEqual(got.Conditions, conditions) {
		t.Fatalf("conditions = %#v, want %#v", got.Conditions, conditions)
	}
}

func TestListDNSMapsFilters(t *testing.T) {
	reader := &stubEventReader{}
	h := &Handler{Events: service.NewEvents(nil, nil, reader, nil, 0, 0)}
	from := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)

	c, rec := newEventContext(t, "/api/v1/events/dns", url.Values{
		"from":      {from.Format(time.RFC3339)},
		"to":        {to.Format(time.RFC3339)},
		"nodeId":    {"node-1"},
		"clientIp":  {"10.0.0.7"},
		"domain":    {"example.com"},
		"container": {"resolver"},
		"blocked":   {"true"},
		"limit":     {"10"},
		"offset":    {"5"},
	})
	if err := h.ListDNS(c); err != nil {
		t.Fatalf("ListDNS: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	got := reader.dns
	if !reader.called || !got.From.Equal(from) || !got.To.Equal(to) {
		t.Fatalf("dns query = %+v, want the request window", got)
	}
	if got.NodeID != "node-1" || got.ClientIP != "10.0.0.7" || got.Domain != "example.com" ||
		got.Container != "resolver" || got.Limit != 10 || got.Offset != 5 {
		t.Fatalf("dns query = %+v, want every request filter forwarded", got)
	}
	if got.Blocked == nil || !*got.Blocked {
		t.Fatalf("blocked = %v, want true", got.Blocked)
	}
}

func TestListTrafficRejectsMalformedFilter(t *testing.T) {
	reader := &stubEventReader{}
	h := &Handler{Events: service.NewEvents(nil, nil, reader, nil, 0, 0)}

	c, rec := newEventContext(t, "/api/v1/events/traffic", url.Values{
		"from":   {time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)},
		"to":     {time.Now().UTC().Format(time.RFC3339)},
		"filter": {"not-json"},
	})
	if err := h.ListTraffic(c); err != nil {
		t.Fatalf("ListTraffic: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("malformed filter reached the event reader")
	}
}
