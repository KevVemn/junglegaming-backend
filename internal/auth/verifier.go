package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/KevVemn/junglegaming-backend/internal/config"
)

var (
	ErrInvalidToken    = errors.New("invalid token")
	ErrUnknownIdentity = errors.New("token does not carry a recognised identity")
)

type Identity struct {
	ProviderID string
	Internal   bool
	Subject    string
}

type Verifier interface {
	Verify(ctx context.Context, rawToken string) (Identity, error)
}

type OIDCVerifier struct {
	verifier      *oidc.IDTokenVerifier
	providerClaim string
	internalClaim string
}

func NewOIDCVerifier(ctx context.Context, cfg config.AuthConfig) (*OIDCVerifier, error) {
	discoveryCtx := ctx
	discoveryURL := cfg.IssuerURL

	if cfg.DiscoveryURL != "" && cfg.DiscoveryURL != cfg.IssuerURL {
		discoveryCtx = oidc.InsecureIssuerURLContext(ctx, cfg.IssuerURL)
		discoveryURL = cfg.DiscoveryURL
	}

	provider, err := oidc.NewProvider(discoveryCtx, discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("discovering oidc provider: %w", err)
	}

	return &OIDCVerifier{
		verifier: provider.Verifier(&oidc.Config{
			ClientID:          cfg.Audience,
			SkipClientIDCheck: cfg.Audience == "",
		}),
		providerClaim: cfg.ProviderClaim,
		internalClaim: cfg.InternalClaim,
	}, nil
}

func (v *OIDCVerifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: cannot read claims", ErrInvalidToken)
	}

	if value, ok := claims[v.providerClaim].(string); ok && value != "" {
		return Identity{ProviderID: value, Subject: token.Subject}, nil
	}

	if value, ok := claims[v.internalClaim].(string); ok && value != "" {
		return Identity{Internal: true, Subject: token.Subject}, nil
	}

	return Identity{}, ErrUnknownIdentity
}

type DisabledVerifier struct{}

func (DisabledVerifier) Verify(context.Context, string) (Identity, error) {
	return Identity{Internal: true}, nil
}
