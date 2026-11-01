package callbacks

import "context"

// SigningSecrets is the port to the credential source for registrar signing secrets (DESIGN
// §12.1). creds.RegistrarSecrets implements it over the warmed credential cache, so secrets stay
// encrypted at rest, are registered for log redaction when loaded and are never decrypted on
// the delivery path; callbacks does not import creds (TECH_SPEC §4.1).
type SigningSecrets interface {
	// SigningSecrets returns the secrets for secretRef (registry.RegistrarEndpoint.SecretRef):
	// the active one first, then the next one during a rotation overlap.
	SigningSecrets(ctx context.Context, secretRef string) ([][]byte, error)
}
