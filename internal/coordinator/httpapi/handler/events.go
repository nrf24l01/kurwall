package handler

import (
	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// ListFlows returns connection history.
func (h *Handler) ListFlows(c echo.Context) error {
	from, err := queryTime(c, "from")
	if err != nil {
		return response.Error(c, err)
	}
	to, err := queryTime(c, "to")
	if err != nil {
		return response.Error(c, err)
	}
	limit, err := queryInt(c, "limit", 50)
	if err != nil {
		return response.Error(c, err)
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return response.Error(c, err)
	}
	protocol, err := queryUint8(c, "protocol")
	if err != nil {
		return response.Error(c, err)
	}
	decision := c.QueryParam("decision")
	if decision != "" && decision != "allowed" && decision != "blocked" {
		return response.Error(c, apperr.Validation("decision must be allowed or blocked"))
	}
	page, err := h.Events.Flows(c.Request().Context(), domain.FlowQuery{
		NodeID: c.QueryParam("nodeId"), SrcIP: c.QueryParam("srcIp"), DstIP: c.QueryParam("dstIp"),
		Domain: c.QueryParam("domain"), Container: c.QueryParam("container"), Decision: decision,
		Protocol: protocol, From: from, To: to, Limit: limit, Offset: offset,
	})
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, page)
}

// ListDNS returns DNS request history.
func (h *Handler) ListDNS(c echo.Context) error {
	from, err := queryTime(c, "from")
	if err != nil {
		return response.Error(c, err)
	}
	to, err := queryTime(c, "to")
	if err != nil {
		return response.Error(c, err)
	}
	limit, err := queryInt(c, "limit", 50)
	if err != nil {
		return response.Error(c, err)
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return response.Error(c, err)
	}
	blocked, err := queryBool(c, "blocked")
	if err != nil {
		return response.Error(c, err)
	}
	page, err := h.Events.DNS(c.Request().Context(), domain.DNSQuery{
		NodeID: c.QueryParam("nodeId"), ClientIP: c.QueryParam("clientIp"),
		Domain: c.QueryParam("domain"), Container: c.QueryParam("container"),
		Blocked: blocked, From: from, To: to, Limit: limit, Offset: offset,
	})
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, page)
}

// ListBlocks returns blocking history.
func (h *Handler) ListBlocks(c echo.Context) error {
	from, err := queryTime(c, "from")
	if err != nil {
		return response.Error(c, err)
	}
	to, err := queryTime(c, "to")
	if err != nil {
		return response.Error(c, err)
	}
	limit, err := queryInt(c, "limit", 50)
	if err != nil {
		return response.Error(c, err)
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return response.Error(c, err)
	}
	kind := c.QueryParam("kind")
	if kind != "" && kind != "ip" && kind != "cidr" && kind != "domain" && kind != "asn" && kind != "dns" {
		return response.Error(c, apperr.Validation("kind must be ip, cidr, domain, asn or dns"))
	}
	page, err := h.Events.Blocks(c.Request().Context(), domain.BlockQuery{
		NodeID: c.QueryParam("nodeId"), Kind: kind, Target: c.QueryParam("target"),
		Domain: c.QueryParam("domain"), Container: c.QueryParam("container"),
		SrcIP: c.QueryParam("srcIp"), DstIP: c.QueryParam("dstIp"),
		From: from, To: to, Limit: limit, Offset: offset,
	})
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, page)
}

// ListTraffic returns allowed connections and blocking decisions in one feed.
func (h *Handler) ListTraffic(c echo.Context) error {
	from, err := queryTime(c, "from")
	if err != nil {
		return response.Error(c, err)
	}
	to, err := queryTime(c, "to")
	if err != nil {
		return response.Error(c, err)
	}
	limit, err := queryInt(c, "limit", 50)
	if err != nil {
		return response.Error(c, err)
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return response.Error(c, err)
	}
	protocol, err := queryUint8(c, "protocol")
	if err != nil {
		return response.Error(c, err)
	}
	decision := c.QueryParam("decision")
	if decision != "" && decision != "allowed" && decision != "blocked" {
		return response.Error(c, apperr.Validation("decision must be allowed or blocked"))
	}
	excludeLocal, err := queryBool(c, "excludeLocal")
	if err != nil {
		return response.Error(c, err)
	}
	page, err := h.Events.Traffic(c.Request().Context(), domain.TrafficQuery{
		NodeID: c.QueryParam("nodeId"), SrcIP: c.QueryParam("srcIp"), DstIP: c.QueryParam("dstIp"),
		IP: c.QueryParam("ip"), Domain: c.QueryParam("domain"), Container: c.QueryParam("container"),
		Decision: decision, ExcludeLocal: excludeLocal != nil && *excludeLocal,
		Protocol: protocol, From: from, To: to, Limit: limit, Offset: offset,
	})
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, page)
}

// Stats returns the dashboard overview.
func (h *Handler) Stats(c echo.Context) error {
	from, err := queryTime(c, "from")
	if err != nil {
		return response.Error(c, err)
	}
	to, err := queryTime(c, "to")
	if err != nil {
		return response.Error(c, err)
	}
	stats, err := h.Events.Stats(c.Request().Context(), from, to)
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, stats)
}
