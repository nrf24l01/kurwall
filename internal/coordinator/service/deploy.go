package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

const (
	defaultDeployKeyTTL = time.Hour
	minDeployKeyTTL     = 5 * time.Minute
	maxDeployKeyTTL     = 24 * time.Hour
	nodeCertificateTTL  = 365 * 24 * time.Hour
)

var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// DeployRepository persists deploy keys and node enrollment.
type DeployRepository interface {
	CreateDeployKey(ctx context.Context, key domain.DeployKey) (domain.DeployKey, error)
	ListDeployKeys(ctx context.Context) ([]domain.DeployKey, error)
	DeleteDeployKey(ctx context.Context, keyID string) (bool, error)
	ConsumeDeployKey(ctx context.Context, tokenHash []byte, remoteIP string, now time.Time) (domain.DeployKey, bool, error)
	NodeExists(ctx context.Context, nodeID string) (bool, error)
	AssignEnrolledNode(ctx context.Context, nodeID string, policyIDs []string) (int64, error)
	CreateRevision(ctx context.Context, actor string) (int64, error)
}

// Deploy issues one-time node enrollment keys and signs client certificates.
type Deploy struct {
	repo       DeployRepository
	notifier   Notifier
	serverCA   *x509.Certificate
	signingCA  *x509.Certificate
	signingKey crypto.Signer
	logger     zerolog.Logger
}

// NewDeploy builds the enrollment service. A nil signing key disables enrollment.
func NewDeploy(repo DeployRepository, notifier Notifier, serverCA, signingCA *x509.Certificate, signingKey crypto.Signer, logger zerolog.Logger) *Deploy {
	return &Deploy{
		repo: repo, notifier: notifier, serverCA: serverCA,
		signingCA: signingCA, signingKey: signingKey, logger: logger,
	}
}

// Enabled reports whether the coordinator can sign agent certificates.
func (s *Deploy) Enabled() bool {
	return s != nil && s.serverCA != nil && s.signingCA != nil && s.signingKey != nil
}

// Create issues a one-time deploy key bound to a node name.
func (s *Deploy) Create(ctx context.Context, nodeName string, policyIDs []string, ttl time.Duration, actor string) (domain.DeployKey, string, error) {
	if !s.Enabled() {
		return domain.DeployKey{}, "", apperr.Unavailable("ENROLLMENT_DISABLED", "node enrollment is disabled: the coordinator has no enrollment CA", nil)
	}
	nodeName = strings.TrimSpace(nodeName)
	if !nodeNamePattern.MatchString(nodeName) {
		return domain.DeployKey{}, "", apperr.Validation("node name must be 1-64 characters: letters, digits, dots, underscores or dashes")
	}
	exists, err := s.repo.NodeExists(ctx, nodeName)
	if err != nil {
		return domain.DeployKey{}, "", err
	}
	if exists {
		return domain.DeployKey{}, "", apperr.Conflict("NODE_EXISTS", "a node with this name already exists; choose a new name")
	}
	if ttl == 0 {
		ttl = defaultDeployKeyTTL
	}
	if ttl < minDeployKeyTTL {
		ttl = minDeployKeyTTL
	}
	if ttl > maxDeployKeyTTL {
		ttl = maxDeployKeyTTL
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		return domain.DeployKey{}, "", apperr.Internal("generate deploy key", err)
	}
	key := domain.DeployKey{
		NodeName: nodeName, TokenHash: hash, PolicyIDs: dedupeStrings(policyIDs),
		CreatedBy: actor, ExpiresAt: time.Now().UTC().Add(ttl),
	}
	stored, err := s.repo.CreateDeployKey(ctx, key)
	if err != nil {
		return domain.DeployKey{}, "", err
	}
	return stored, token, nil
}

// List returns recent keys without plaintext tokens.
func (s *Deploy) List(ctx context.Context) ([]domain.DeployKey, error) {
	return s.repo.ListDeployKeys(ctx)
}

// Revoke deletes an unused key.
func (s *Deploy) Revoke(ctx context.Context, keyID string) error {
	deleted, err := s.repo.DeleteDeployKey(ctx, keyID)
	if err != nil {
		return err
	}
	if !deleted {
		return apperr.NotFound("DEPLOY_KEY_NOT_FOUND", "deploy key not found or already used")
	}
	return nil
}

// Enroll validates a deploy key, signs the CSR and assigns the node.
func (s *Deploy) Enroll(ctx context.Context, token string, csrPEM []byte, remoteIP string) ([]byte, []byte, string, error) {
	if !s.Enabled() {
		return nil, nil, "", apperr.Unavailable("ENROLLMENT_DISABLED", "node enrollment is disabled", nil)
	}
	if !auth.ValidTokenFormat(token) {
		return nil, nil, "", apperr.Validation("deploy key is invalid")
	}
	key, found, err := s.repo.ConsumeDeployKey(ctx, auth.HashToken(token), remoteIP, time.Now().UTC())
	if err != nil {
		return nil, nil, "", err
	}
	if !found {
		return nil, nil, "", apperr.Validation("deploy key is invalid, expired, or already used")
	}

	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, nil, "", apperr.Validation("request body must be a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, nil, "", apperr.Validation("certificate request is invalid")
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, "", apperr.Internal("generate certificate serial", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: key.NodeName, Organization: []string{"Kurwall node"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(nodeCertificateTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, s.signingCA, csr.PublicKey, s.signingKey)
	if err != nil {
		return nil, nil, "", apperr.Internal("sign node certificate", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.serverCA.Raw})

	assignments, err := s.repo.AssignEnrolledNode(ctx, key.NodeName, key.PolicyIDs)
	if err != nil {
		return nil, nil, "", err
	}
	if assignments > 0 {
		if _, err := s.repo.CreateRevision(ctx, "enroll:"+key.NodeName); err != nil {
			s.logger.Warn().Err(err).Str("node", key.NodeName).Msg("failed to publish enrollment revision")
		} else if s.notifier != nil {
			s.notifier.PushPolicy(ctx, []string{key.NodeName})
		}
	}
	s.logger.Info().Str("node", key.NodeName).Str("remote_ip", remoteIP).Msg("node certificate issued")
	return certPEM, caPEM, key.NodeName, nil
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
