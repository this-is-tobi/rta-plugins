package main

import (
	"strings"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
)

// A refused token is answered by Vault as one error holding a go-multierror's
// text: "2 errors occurred:", then each reason on a line behind a tab and an
// asterisk. It went into the message as it came, so the first thing an agent
// read of a refusal was a list with newlines in it, in a field that is a line.
func TestARefusalIsOneLineWhateverShapeVaultAnswersIn(t *testing.T) {
	r := req(t, "vault.kv.list", map[string]any{"address": "http://vault.internal:8200"})
	for name, errs := range map[string][]string{
		"a multierror":    {"2 errors occurred:\n\t* permission denied\n\t* invalid token\n\n"},
		"one reason":      {"1 error occurred:\n\t* permission denied\n\n"},
		"plain reasons":   {"permission denied", "invalid token"},
		"mixed":           {"permission denied", "2 errors occurred:\n\t* a\n\t* b\n\n"},
		"an empty answer": nil,
	} {
		t.Run(name, func(t *testing.T) {
			verr := classify(&vaultapi.ResponseError{StatusCode: 403, Errors: errs}, r)
			if strings.ContainsAny(verr.Message, "\n\t") {
				t.Errorf("message = %q, want one line", verr.Message)
			}
			if name == "a multierror" && !strings.HasSuffix(verr.Message, "refused: permission denied; invalid token") {
				t.Errorf("message = %q, want the reasons joined", verr.Message)
			}
		})
	}
}
