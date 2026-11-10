package handler

import (
	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// ListNodes returns all registered agents.
func (h *Handler) ListNodes(c echo.Context) error {
	nodes, err := h.Nodes.List(c.Request().Context())
	if err != nil {
		return response.Error(c, err)
	}
	if nodes == nil {
		nodes = []domain.Node{}
	}
	return response.OK(c, map[string]any{"items": nodes})
}

// GetNode returns one node.
func (h *Handler) GetNode(c echo.Context) error {
	node, err := h.Nodes.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, node)
}

// UpdateNode renames a node or replaces its labels.
func (h *Handler) UpdateNode(c echo.Context) error {
	var body domain.NodeUpdate
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	if body.Name == nil && body.Labels == nil {
		return response.Error(c, apperr.Validation("name or labels must be provided"))
	}
	node, err := h.Nodes.Update(c.Request().Context(), c.Param("id"), body)
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, node)
}

type assignPoliciesRequest struct {
	PolicyIDs []string `json:"policyIds"`
}

// AssignNodePolicies replaces the policy assignments of a node.
func (h *Handler) AssignNodePolicies(c echo.Context) error {
	var body assignPoliciesRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	if len(body.PolicyIDs) > 100 {
		return response.Error(c, apperr.Validation("at most 100 policies can be assigned"))
	}
	node, err := h.Nodes.AssignPolicies(c.Request().Context(), c.Param("id"), body.PolicyIDs, actor(c))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, node)
}

// GetNodeSnapshot returns the effective policy of a node for troubleshooting.
func (h *Handler) GetNodeSnapshot(c echo.Context) error {
	snapshot, err := h.Nodes.Snapshot(c.Request().Context(), c.Param("id"))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, snapshot)
}
