package creds

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Auth scheme names, as vendor definitions spell them in `auth:` (DESIGN §8.3, §9).
const (
	SchemeStaticHeaders = "static_headers"
	SchemeBearer        = "bearer"
	SchemeBasic         = "basic"
)

// Schemes lists the auth schemes this build implements.
func Schemes() []string { return []string{SchemeBasic, SchemeBearer, SchemeStaticHeaders} }

// ValidateScheme reports ErrUnknownScheme for a name this build does not implement, so a
// snapshot build can reject the vendor instead of failing at request time.
func ValidateScheme(name string) error {
	if !slices.Contains(Schemes(), name) {
		return fmt.Errorf("%w %q (implemented: %s)", ErrUnknownScheme, name, strings.Join(Schemes(), ", "))
	}
	return nil
}

// ApplyVendor applies the auth scheme declared by vendor v. It is Apply(v.Auth, ...): the
// scheme always comes from the vendor definition, never from the transformer.
func ApplyVendor(v *registry.Vendor, c Credentials, req *core.VendorRequest) error {
	return Apply(v.Auth, c, req)
}

// Apply runs the auth scheme on a request the transformer has already built. It runs after
// the request.transformed audit snapshot is taken, and it never mutates the header map it was
// given: when it injects, it installs a new map, so a snapshot holding the old map does not
// see injected values.
//
//   - static_headers: the transformer maps creds.* into headers itself (e.g. CleverTap
//     X-CleverTap-Passcode). Apply injects nothing; it checks that every credential value is
//     present as a header value, so a transformer that forgot one fails here, not at the vendor.
//   - bearer: Authorization: Bearer <values.token>.
//   - basic: Authorization: Basic base64(<values.username>:<values.password>).
//
// Errors name credential keys, never values.
func Apply(scheme string, c Credentials, req *core.VendorRequest) error {
	switch scheme {
	case SchemeStaticHeaders:
		return checkStaticHeaders(c, req)
	case SchemeBearer:
		token, err := need(c, "token")
		if err != nil {
			return err
		}
		setAuthorization(req, "Bearer "+token)
		return nil
	case SchemeBasic:
		user, err := need(c, "username")
		if err != nil {
			return err
		}
		pass, err := need(c, "password")
		if err != nil {
			return err
		}
		setAuthorization(req, "Basic "+basicToken(user, pass))
		return nil
	default:
		return ValidateScheme(scheme)
	}
}

func need(c Credentials, key string) (string, error) {
	v := c.Values[key]
	if v == "" {
		return "", fmt.Errorf("%w: account %q has no %q", ErrMissingCredential, c.AccountID, key)
	}
	return v, nil
}

func checkStaticHeaders(c Credentials, req *core.VendorRequest) error {
	if len(c.Values) == 0 {
		return fmt.Errorf("%w: account %q has no credential values", ErrMissingCredential, c.AccountID)
	}
	var missing []string
	for k, v := range c.Values {
		found := false
		for _, hv := range req.Headers {
			if hv == v {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("%w: static_headers: the request carries no header with credential %s of account %q (map it with creds.<key> in the transformer headers)",
			ErrMissingCredential, strings.Join(missing, ", "), c.AccountID)
	}
	return nil
}

// setAuthorization installs a copy of the headers with Authorization replaced (any casing).
func setAuthorization(req *core.VendorRequest, value string) {
	h := make(map[string]string, len(req.Headers)+1)
	for k, v := range req.Headers {
		if !strings.EqualFold(k, "Authorization") {
			h[k] = v
		}
	}
	h["Authorization"] = value
	req.Headers = h
}
