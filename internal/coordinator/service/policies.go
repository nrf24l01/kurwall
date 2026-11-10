package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

const (
	maxBatchRules          = 200
	asnResolveWorkers      = 4
	asnBatchResolveTimeout = 30 * time.Second
)

// Policies manages policies, rules and revision publication.
type Policies struct {
	repo     domain.Repository
	notifier Notifier
	asn      *ASN
}

// NewPolicies builds the policy service.
func NewPolicies(repo domain.Repository, notifier Notifier, asn *ASN) *Policies {
	return &Policies{repo: repo, notifier: notifier, asn: asn}
}

// List returns all policies.
func (s *Policies) List(ctx context.Context) ([]domain.Policy, error) {
	return s.repo.ListPolicies(ctx)
}

// Get returns a policy with its rules.
func (s *Policies) Get(ctx context.Context, id string) (domain.Policy, error) {
	policy, err := s.repo.GetPolicy(ctx, id)
	if err != nil {
		return domain.Policy{}, err
	}
	rules, err := s.repo.ListRules(ctx, id)
	if err != nil {
		return domain.Policy{}, err
	}
	if rules == nil {
		rules = []domain.Rule{}
	}
	policy.Rules = s.enrichASN(ctx, rules)
	return policy, nil
}

// Create validates and creates a policy.
func (s *Policies) Create(ctx context.Context, input domain.PolicyInput, actor string) (domain.Policy, error) {
	input, err := validatePolicyInput(input)
	if err != nil {
		return domain.Policy{}, err
	}
	policy, err := s.repo.CreatePolicy(ctx, input)
	if err != nil {
		return domain.Policy{}, err
	}
	if _, err := s.repo.CreateRevision(ctx, actor); err != nil {
		return domain.Policy{}, err
	}
	return policy, nil
}

// Update validates and updates a policy, then pushes it to affected nodes.
func (s *Policies) Update(ctx context.Context, id string, input domain.PolicyInput, actor string) (domain.Policy, error) {
	input, err := validatePolicyInput(input)
	if err != nil {
		return domain.Policy{}, err
	}
	policy, err := s.repo.UpdatePolicy(ctx, id, input)
	if err != nil {
		return domain.Policy{}, err
	}
	if err := s.publish(ctx, actor, id); err != nil {
		return domain.Policy{}, err
	}
	return policy, nil
}

// Delete removes a policy and republishes affected nodes.
func (s *Policies) Delete(ctx context.Context, id string, actor string) error {
	nodeIDs, err := s.repo.NodeIDsAffectedByPolicy(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeletePolicy(ctx, id); err != nil {
		return err
	}
	if _, err := s.repo.CreateRevision(ctx, actor); err != nil {
		return err
	}
	if s.notifier != nil {
		s.notifier.PushPolicy(ctx, nodeIDs)
	}
	return nil
}

// ListRules returns the rules of a policy.
func (s *Policies) ListRules(ctx context.Context, policyID string) ([]domain.Rule, error) {
	if _, err := s.repo.GetPolicy(ctx, policyID); err != nil {
		return nil, err
	}
	rules, err := s.repo.ListRules(ctx, policyID)
	if err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []domain.Rule{}
	}
	return s.enrichASN(ctx, rules), nil
}

// RuleFailure describes one rule that was not created because its target could
// not be resolved.
type RuleFailure struct {
	Value   string `json:"value"`
	Message string `json:"message"`
}

// CreateRulesResult reports a partial-successful bulk rule creation.
type CreateRulesResult struct {
	Rules   []domain.Rule `json:"items"`
	Skipped []string      `json:"skipped"`
	Failed  []RuleFailure `json:"failed"`
}

// CreateRule adds a validated rule to a policy.
func (s *Policies) CreateRule(ctx context.Context, policyID string, input domain.RuleInput, actor string) (domain.Rule, error) {
	input, err := validateRuleInput(input)
	if err != nil {
		return domain.Rule{}, err
	}
	if input.Kind == domain.RuleKindASN {
		if err := s.resolveASN(ctx, input.Value); err != nil {
			return domain.Rule{}, err
		}
	}
	rule, err := s.repo.CreateRule(ctx, policyID, input)
	if err != nil {
		return domain.Rule{}, err
	}
	rule.PrefixCount = s.asnPrefixCount(ctx, rule)
	if err := s.publish(ctx, actor, policyID); err != nil {
		return domain.Rule{}, err
	}
	return rule, nil
}

// CreateRules adds several rules to a policy in one revision. ASN resolution
// runs with bounded concurrency; targets that cannot be resolved are reported
// without preventing the other rules from being created.
func (s *Policies) CreateRules(ctx context.Context, policyID string, inputs []domain.RuleInput, actor string) (CreateRulesResult, error) {
	if len(inputs) == 0 {
		return CreateRulesResult{}, apperr.Validation("at least one rule is required")
	}
	if len(inputs) > maxBatchRules {
		return CreateRulesResult{}, apperr.Validation(fmt.Sprintf("at most %d rules can be created at once", maxBatchRules))
	}

	normalized := make([]domain.RuleInput, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	hasASN := false
	for _, input := range inputs {
		input, err := validateRuleInput(input)
		if err != nil {
			return CreateRulesResult{}, err
		}
		key := input.Kind + "\x00" + input.Value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, input)
		if input.Kind == domain.RuleKindASN {
			hasASN = true
		}
	}
	if hasASN && (s.asn == nil || !s.asn.Enabled()) {
		return CreateRulesResult{}, apperr.Validation("ASN resolution is disabled on this coordinator")
	}

	failedByIndex := make([]*RuleFailure, len(normalized))
	var mu sync.Mutex
	var group errgroup.Group
	group.SetLimit(asnResolveWorkers)
	resolveCtx, cancel := context.WithTimeout(ctx, asnBatchResolveTimeout)
	defer cancel()
	for i, input := range normalized {
		if input.Kind != domain.RuleKindASN {
			continue
		}
		i, input := i, input
		group.Go(func() error {
			err := s.resolveASN(resolveCtx, input.Value)
			if err == nil {
				return nil
			}
			message := "the autonomous system could not be resolved"
			if appErr, ok := apperr.As(err); ok {
				message = appErr.Message
			}
			mu.Lock()
			failedByIndex[i] = &RuleFailure{Value: input.Value, Message: message}
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()

	result := CreateRulesResult{Rules: []domain.Rule{}, Skipped: []string{}, Failed: []RuleFailure{}}
	ready := make([]domain.RuleInput, 0, len(normalized))
	for i, input := range normalized {
		if failedByIndex[i] != nil {
			result.Failed = append(result.Failed, *failedByIndex[i])
			continue
		}
		ready = append(ready, input)
	}
	if len(ready) == 0 {
		return result, nil
	}

	rules, skipped, err := s.repo.CreateRules(ctx, policyID, ready)
	if err != nil {
		return CreateRulesResult{}, err
	}
	result.Rules = s.enrichASN(ctx, rules)
	result.Skipped = skipped
	if len(rules) > 0 {
		if err := s.publish(ctx, actor, policyID); err != nil {
			return CreateRulesResult{}, err
		}
	}
	return result, nil
}

// UpdateRule updates a rule and republishes affected nodes.
func (s *Policies) UpdateRule(ctx context.Context, policyID, ruleID string, input domain.RuleInput, actor string) (domain.Rule, error) {
	input, err := validateRuleInput(input)
	if err != nil {
		return domain.Rule{}, err
	}
	if input.Kind == domain.RuleKindASN {
		if err := s.resolveASN(ctx, input.Value); err != nil {
			return domain.Rule{}, err
		}
	}
	rule, err := s.repo.UpdateRule(ctx, policyID, ruleID, input)
	if err != nil {
		return domain.Rule{}, err
	}
	rule.PrefixCount = s.asnPrefixCount(ctx, rule)
	if err := s.publish(ctx, actor, policyID); err != nil {
		return domain.Rule{}, err
	}
	return rule, nil
}

// DeleteRule removes a rule and republishes affected nodes.
func (s *Policies) DeleteRule(ctx context.Context, policyID, ruleID string, actor string) error {
	if err := s.repo.DeleteRule(ctx, policyID, ruleID); err != nil {
		return err
	}
	return s.publish(ctx, actor, policyID)
}

func (s *Policies) publish(ctx context.Context, actor, policyID string) error {
	nodeIDs, err := s.repo.NodeIDsAffectedByPolicy(ctx, policyID)
	if err != nil {
		return err
	}
	if _, err := s.repo.CreateRevision(ctx, actor); err != nil {
		return err
	}
	if s.notifier != nil {
		s.notifier.PushPolicy(ctx, nodeIDs)
	}
	return nil
}

func (s *Policies) resolveASN(ctx context.Context, value string) error {
	if s.asn == nil || !s.asn.Enabled() {
		return apperr.Validation("ASN resolution is disabled on this coordinator")
	}
	asn, err := ruleutil.ParseASN(value)
	if err != nil {
		return apperr.Validation(err.Error())
	}
	cache, err := s.asn.Ensure(ctx, asn, false)
	if err != nil && len(cache.Prefixes) == 0 {
		return apperr.Unavailable("ASN_RESOLUTION_FAILED", "the autonomous system could not be resolved", err)
	}
	return nil
}

func (s *Policies) asnPrefixCount(ctx context.Context, rule domain.Rule) int {
	if rule.Kind != domain.RuleKindASN {
		return 0
	}
	asn, err := ruleutil.ParseASN(rule.Value)
	if err != nil {
		return 0
	}
	cache, found, err := s.repo.GetASNCache(ctx, asn)
	if err != nil || !found {
		return 0
	}
	return len(cache.Prefixes)
}

func (s *Policies) enrichASN(ctx context.Context, rules []domain.Rule) []domain.Rule {
	for i := range rules {
		rules[i].PrefixCount = s.asnPrefixCount(ctx, rules[i])
	}
	return rules
}

func validatePolicyInput(input domain.PolicyInput) (domain.PolicyInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 80 {
		return input, apperr.Validation("name must be between 1 and 80 characters")
	}
	if len(input.Description) > 500 {
		return input, apperr.Validation("description must be at most 500 characters")
	}
	return input, nil
}

func validateRuleInput(input domain.RuleInput) (domain.RuleInput, error) {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Value = strings.TrimSpace(input.Value)
	input.Note = strings.TrimSpace(input.Note)
	if input.Action == "" {
		input.Action = domain.RuleActionBlock
	}
	if input.Action != domain.RuleActionBlock {
		return input, apperr.Validation("action must be block")
	}
	if len(input.Note) > 300 {
		return input, apperr.Validation("note must be at most 300 characters")
	}
	normalized, err := ruleutil.NormalizeRule(input.Kind, input.Value)
	if err != nil {
		appErr := apperr.Validation("invalid rule value")
		appErr.Field("value", err.Error(), input.Value)
		return input, appErr
	}
	input.Value = normalized
	return input, nil
}
