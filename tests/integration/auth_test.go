//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/KevVemn/junglegaming-backend/internal/auth"
	"github.com/KevVemn/junglegaming-backend/internal/config"
)

type keycloakEnv struct {
	issuerURL string
	tokenURL  string
	verifier  auth.Verifier
}

var (
	keycloakOnce sync.Once
	keycloakRef  *keycloakEnv
	keycloakErr  error
)

func requireKeycloak(t *testing.T) *keycloakEnv {
	t.Helper()

	keycloakOnce.Do(func() {
		keycloakRef, keycloakErr = startKeycloak(context.Background())
	})

	if keycloakErr != nil {
		t.Fatalf("starting keycloak failed: %v", keycloakErr)
	}
	return keycloakRef
}

func startKeycloak(ctx context.Context) (*keycloakEnv, error) {
	realmPath, err := filepath.Abs(filepath.Join("..", "..", "deploy", "keycloak", "realm-export.json"))
	if err != nil {
		return nil, err
	}

	req := testcontainers.ContainerRequest{
		Image:        "quay.io/keycloak/keycloak:26.0",
		ExposedPorts: []string{"8080/tcp"},
		Cmd:          []string{"start-dev", "--import-realm"},
		Env: map[string]string{
			"KC_BOOTSTRAP_ADMIN_USERNAME": "admin",
			"KC_BOOTSTRAP_ADMIN_PASSWORD": "admin",
			"KC_HTTP_ENABLED":             "true",
		},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      realmPath,
				ContainerFilePath: "/opt/keycloak/data/import/realm.json",
				FileMode:          0o644,
			},
		},
		WaitingFor: wait.ForHTTP("/realms/junglegaming/.well-known/openid-configuration").
			WithPort("8080/tcp").
			WithStartupTimeout(180 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, err
	}

	host, err := container.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := container.MappedPort(ctx, "8080/tcp")
	if err != nil {
		return nil, err
	}

	issuer := fmt.Sprintf("http://%s:%s/realms/junglegaming", host, port.Port())

	verifier, err := auth.NewOIDCVerifier(ctx, config.AuthConfig{
		IssuerURL:     issuer,
		Audience:      "wagering-api",
		ProviderClaim: "provider_id",
		InternalClaim: "scope_internal",
		Enabled:       true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating verifier: %w", err)
	}

	return &keycloakEnv{
		issuerURL: issuer,
		tokenURL:  issuer + "/protocol/openid-connect/token",
		verifier:  verifier,
	}, nil
}

func (k *keycloakEnv) token(t *testing.T, clientID, clientSecret string) string {
	t.Helper()

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	resp, err := http.Post(k.tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("requesting token failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token request returned %d: %s", resp.StatusCode, body)
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decoding token response failed: %v", err)
	}
	if parsed.AccessToken == "" {
		t.Fatal("token response carried no access_token")
	}
	return parsed.AccessToken
}

func TestKeycloakIssuesTokenWithProviderClaim(t *testing.T) {
	kc := requireKeycloak(t)

	cases := []struct {
		client     string
		secret     string
		wantID     string
		wantInline bool
	}{
		{"provider-a", "provider-a-secret", "provider-a", false},
		{"provider-b", "provider-b-secret", "provider-b", false},
		{"wallet-admin", "wallet-admin-secret", "", true},
	}

	for _, c := range cases {
		t.Run(c.client, func(t *testing.T) {
			raw := kc.token(t, c.client, c.secret)

			identity, err := kc.verifier.Verify(context.Background(), raw)
			if err != nil {
				t.Fatalf("verifying token failed: %v", err)
			}
			if identity.ProviderID != c.wantID {
				t.Errorf("providerId = %q, want %q", identity.ProviderID, c.wantID)
			}
			if identity.Internal != c.wantInline {
				t.Errorf("internal = %v, want %v", identity.Internal, c.wantInline)
			}
		})
	}
}

func TestVerifierRejectsInvalidTokens(t *testing.T) {
	kc := requireKeycloak(t)

	cases := map[string]string{
		"empty":              "",
		"garbage":            "not-a-token",
		"malformed jwt":      "aaa.bbb.ccc",
		"tampered signature": kc.token(t, "provider-a", "provider-a-secret") + "tampered",
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := kc.verifier.Verify(context.Background(), token); err == nil {
				t.Error("an invalid token should have been rejected")
			}
		})
	}
}

func TestVerifierRejectsWrongClientSecret(t *testing.T) {
	kc := requireKeycloak(t)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", "provider-a")
	form.Set("client_secret", "wrong-secret")

	resp, err := http.Post(kc.tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("requesting token failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("keycloak should not issue a token for a wrong secret")
	}
}

func TestProvidersAreIsolatedFromEachOther(t *testing.T) {
	kc := requireKeycloak(t)

	identityA, err := kc.verifier.Verify(context.Background(), kc.token(t, "provider-a", "provider-a-secret"))
	if err != nil {
		t.Fatalf("verifying provider-a failed: %v", err)
	}
	identityB, err := kc.verifier.Verify(context.Background(), kc.token(t, "provider-b", "provider-b-secret"))
	if err != nil {
		t.Fatalf("verifying provider-b failed: %v", err)
	}

	if identityA.ProviderID == identityB.ProviderID {
		t.Fatal("both providers resolved to the same identity")
	}

	wallet := openWallet(t, "200.00")
	cmd := betCommand(wallet, "iso-"+wallet.ID().String(), "20.00")
	cmd.ProviderID = identityA.ProviderID
	cmd.IdempotencyKey = identityA.ProviderID + ":" + cmd.ExternalTransactionID

	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("submitting as provider-a failed: %v", err)
	}

	transaction, err := sharedEnv.WageringSvc.GetByExternalID(
		context.Background(), identityA.ProviderID, cmd.ExternalTransactionID)
	if err != nil {
		t.Fatalf("reading transaction failed: %v", err)
	}

	if _, err := sharedEnv.WageringSvc.GetByID(
		context.Background(), transaction.ID(), identityB.ProviderID); err == nil {
		t.Error("provider-b should not be able to read a transaction owned by provider-a")
	}

	if _, err := sharedEnv.WageringSvc.GetByID(
		context.Background(), transaction.ID(), identityA.ProviderID); err != nil {
		t.Errorf("provider-a should be able to read its own transaction: %v", err)
	}
}
