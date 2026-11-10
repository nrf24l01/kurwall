package handler

import (
	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

type policyRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
}

func (r policyRequest) input() domain.PolicyInput {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return domain.PolicyInput{Name: r.Name, Description: r.Description, Enabled: enabled}
}

type ruleRequest struct {
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Action  string `json:"action"`
	Note    string `json:"note"`
	Enabled *bool  `json:"enabled"`
}

type rulesBatchRequest struct {
	Rules []ruleRequest `json:"rules"`
}

func (r ruleRequest) input() domain.RuleInput {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return domain.RuleInput{Kind: r.Kind, Value: r.Value, Action: r.Action, Note: r.Note, Enabled: enabled}
}

// ListPolicies returns all policies.
func (h *Handler) ListPolicies(c echo.Context) error {
	policies, err := h.Policies.List(c.Request().Context())
	if err != nil {
		return response.Error(c, err)
	}
	if policies == nil {
		policies = []domain.Policy{}
	}
	return response.OK(c, map[string]any{"items": policies})
}

// GetPolicy returns a policy with its rules.
func (h *Handler) GetPolicy(c echo.Context) error {
	policy, err := h.Policies.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, policy)
}

// CreatePolicy creates a policy.
func (h *Handler) CreatePolicy(c echo.Context) error {
	var body policyRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	policy, err := h.Policies.Create(c.Request().Context(), body.input(), actor(c))
	if err != nil {
		return response.Error(c, err)
	}
	return response.Created(c, policy)
}

// UpdatePolicy updates a policy.
func (h *Handler) UpdatePolicy(c echo.Context) error {
	var body policyRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	policy, err := h.Policies.Update(c.Request().Context(), c.Param("id"), body.input(), actor(c))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, policy)
}

// DeletePolicy removes a policy.
func (h *Handler) DeletePolicy(c echo.Context) error {
	if err := h.Policies.Delete(c.Request().Context(), c.Param("id"), actor(c)); err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, map[string]any{"deleted": true})
}

// ListRules returns the rules of a policy.
func (h *Handler) ListRules(c echo.Context) error {
	rules, err := h.Policies.ListRules(c.Request().Context(), c.Param("id"))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, map[string]any{"items": rules})
}

// CreateRule adds a rule to a policy.
func (h *Handler) CreateRule(c echo.Context) error {
	var body ruleRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	rule, err := h.Policies.CreateRule(c.Request().Context(), c.Param("id"), body.input(), actor(c))
	if err != nil {
		return response.Error(c, err)
	}
	return response.Created(c, rule)
}

// CreateRules adds several rules to a policy in one revision.
func (h *Handler) CreateRules(c echo.Context) error {
	var body rulesBatchRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	inputs := make([]domain.RuleInput, 0, len(body.Rules))
	for _, item := range body.Rules {
		inputs = append(inputs, item.input())
	}
	result, err := h.Policies.CreateRules(c.Request().Context(), c.Param("id"), inputs, actor(c))
	if err != nil {
		return response.Error(c, err)
	}
	return response.Created(c, result)
}

// UpdateRule updates a rule.
func (h *Handler) UpdateRule(c echo.Context) error {
	var body ruleRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	if body.Kind == "" || body.Value == "" {
		return response.Error(c, apperr.Validation("kind and value are required"))
	}
	rule, err := h.Policies.UpdateRule(c.Request().Context(), c.Param("id"), c.Param("ruleId"), body.input(), actor(c))
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, rule)
}

// DeleteRule removes a rule.
func (h *Handler) DeleteRule(c echo.Context) error {
	if err := h.Policies.DeleteRule(c.Request().Context(), c.Param("id"), c.Param("ruleId"), actor(c)); err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, map[string]any{"deleted": true})
}
