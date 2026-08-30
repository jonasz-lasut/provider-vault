/*
Copyright 2021 Upbound Inc.
*/

package clients

import (
	"context"
	"os"
	"testing"

	"github.com/crossplane/upjet/v2/pkg/terraform"
	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/vault/api"

	tfvaultschema "github.com/hashicorp/terraform-provider-vault/schema"
	tfvault "github.com/hashicorp/terraform-provider-vault/vault"

	namespacedv1beta1 "github.com/upbound/provider-vault/v4/apis/namespaced/v1beta1"
)

// testAddress is the Vault address these tests hand to the ProviderConfig. It
// is never dialed: TestSetProviderConfiguration only inspects the map that is
// built from it.
const testAddress = "http://vault:8200"

func ptr[T any](v T) *T { return &v }

// TestSetProviderConfiguration pins which ProviderConfig fields reach the
// Terraform provider. A field written with its zero value when the user left it
// unset is indistinguishable from the user asking for that value, so it
// overrides whatever default the Terraform provider documents.
func TestSetProviderConfiguration(t *testing.T) {
	cases := map[string]struct {
		spec namespacedv1beta1.ProviderConfigSpec
		want terraform.ProviderConfiguration
	}{
		"defaults: only the always-written keys": {
			spec: namespacedv1beta1.ProviderConfigSpec{Address: testAddress},
			want: terraform.ProviderConfiguration{
				keyAddress:             testAddress,
				keyAddAddressToEnv:     false,
				keySkipTLSVerify:       false,
				keySkipChildToken:      false,
				keyMaxLeaseTTLSeconds:  0,
				keyMaxRetries:          0,
				keyMaxRetriesCcc:       0,
				keySkipGetVaultVersion: false,
			},
		},
		"skipChildToken set": {
			spec: namespacedv1beta1.ProviderConfigSpec{Address: testAddress, SkipChildToken: true},
			want: terraform.ProviderConfiguration{
				keyAddress:             testAddress,
				keyAddAddressToEnv:     false,
				keySkipTLSVerify:       false,
				keySkipChildToken:      true,
				keyMaxLeaseTTLSeconds:  0,
				keyMaxRetries:          0,
				keyMaxRetriesCcc:       0,
				keySkipGetVaultVersion: false,
			},
		},
		"setNamespaceFromToken explicitly false": {
			spec: namespacedv1beta1.ProviderConfigSpec{
				Address:               testAddress,
				SetNamespaceFromToken: ptr(false),
			},
			want: terraform.ProviderConfiguration{
				keyAddress:               testAddress,
				keyAddAddressToEnv:       false,
				keySkipTLSVerify:         false,
				keySkipChildToken:        false,
				keyMaxLeaseTTLSeconds:    0,
				keyMaxRetries:            0,
				keyMaxRetriesCcc:         0,
				keySkipGetVaultVersion:   false,
				keySetNamespaceFromToken: false,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ps := terraform.Setup{}
			setProviderConfiguration(&tc.spec, &ps)
			if diff := cmp.Diff(tc.want, ps.Configuration); diff != "" {
				t.Errorf("setProviderConfiguration() -want +got:\n%s", diff)
			}
		})
	}
}

// TestConfigureNoForkVaultClient exercises the path a running provider takes:
// a ProviderConfig spec becomes ps.Configuration, which schema.Provider.Configure
// consumes without a terraform plan/apply cycle. That is where boolean provider
// config fields were being dropped, so it needs a real Vault to be meaningful.
//
// Set VAULT_ADDR and VAULT_TOKEN to run it, for example:
//
//	docker run -d -p 8200:8200 -e VAULT_DEV_ROOT_TOKEN_ID=root hashicorp/vault
//	VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root go test ./internal/clients/...
func TestConfigureNoForkVaultClient(t *testing.T) {
	addr, token := os.Getenv("VAULT_ADDR"), os.Getenv("VAULT_TOKEN")
	if addr == "" || token == "" {
		t.Skip("VAULT_ADDR and VAULT_TOKEN must be set to run this test")
	}

	cases := map[string]struct {
		spec           namespacedv1beta1.ProviderConfigSpec
		wantChildToken bool
	}{
		"skipChildToken false: an ephemeral child token is minted": {
			spec:           namespacedv1beta1.ProviderConfigSpec{Address: addr},
			wantChildToken: true,
		},
		"skipChildToken true: the supplied token is used as-is": {
			spec:           namespacedv1beta1.ProviderConfigSpec{Address: addr, SkipChildToken: true},
			wantChildToken: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ps := terraform.Setup{}
			setProviderConfiguration(&tc.spec, &ps)
			ps.Configuration[keyToken] = token

			// non-pointer, matching how TerraformSetupBuilder calls it
			p := *tfvaultschema.NewProvider(tfvault.Provider()).SchemaProvider()
			if err := configureNoForkVaultClient(context.Background(), &ps, p); err != nil {
				t.Fatalf("configureNoForkVaultClient() error = %v", err)
			}

			effective := effectiveToken(t, ps)
			if gotChild := effective != token; gotChild != tc.wantChildToken {
				t.Errorf("child token minted = %v, want %v", gotChild, tc.wantChildToken)
			}
		})
	}
}

// effectiveToken reads the token the configured provider actually ended up
// using. ps.Meta holds the Terraform provider's *ProviderMeta, which lives in
// an internal package we cannot name, so match on its method set instead.
func effectiveToken(t *testing.T, ps terraform.Setup) string {
	t.Helper()

	meta, ok := ps.Meta.(interface {
		GetClient() (*api.Client, error)
	})
	if !ok {
		t.Fatalf("ps.Meta does not expose GetClient(), got %T", ps.Meta)
	}

	client, err := meta.GetClient()
	if err != nil {
		t.Fatalf("GetClient() error = %v", err)
	}
	return client.Token()
}
