package adminapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Error codes added with ISFC mode (hub-admin 2.1.0). Unlike the older upper-case codes these two
// are lower-case in the contract.
const (
	// CodeAuthzUnavailable is 503: the policy engine could not decide, the request is refused.
	CodeAuthzUnavailable = "authz_unavailable"
	// CodeTenantManagedByISFC is 409 from createTenant when tenants come from ISFC.
	CodeTenantManagedByISFC = "tenant_managed_by_isfc"
)

// authzUnavailable is the fail-closed answer when OPA cannot decide.
func authzUnavailable() *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: CodeAuthzUnavailable, Retryable: true, RetryAfterMS: core.Ptr[int64](1000),
		Message: "Authorization is temporarily unavailable"}
}

// controlError maps a control-plane error to the admin envelope; unknown errors stay as they
// are (fail logs them and answers 500).
func controlError(err error) error {
	var blocked *control.RetireBlockedError
	var problems registry.ConfigErrors
	switch {
	case err == nil:
		return nil
	case errors.Is(err, control.ErrMissingAuthor), errors.Is(err, control.ErrMissingReason):
		return badRequest("/reason", err.Error())
	case errors.As(err, &blocked):
		return &Error{Status: http.StatusConflict, Code: CodeRetireBlocked, Retryable: true, RetryAfterMS: core.Ptr[int64](600_000),
			Message: "Account still referenced by pinned requests or open vendor sessions", Details: []Detail{},
			Meta: map[string]any{"pinned_requests": blocked.Pinned, "open_sessions": blocked.Sessions, "active_versions": []int{}}}
	case errors.As(err, &problems):
		return validationFailed(problems)
	case errors.Is(err, control.ErrNotFound), errors.Is(err, control.ErrUnknownAccount):
		return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: upperFirst(err.Error())}
	case errors.Is(err, control.ErrConflict), errors.Is(err, creds.ErrRotationConflict):
		return &Error{Status: http.StatusConflict, Code: CodeConflict, Message: upperFirst(err.Error())}
	}
	return err
}

// validationFailed is 422 PUBLISH_VALIDATION_FAILED with the report in meta (DESIGN §6.4).
func validationFailed(problems registry.ConfigErrors) *Error {
	details := make([]Detail, 0, len(problems))
	checks := make([]map[string]any, 0, len(problems))
	for _, p := range problems {
		ptr, check := pointer(p.Path), checkOf(p)
		msg := p.Message
		if p.File != "" {
			msg = p.File + ": " + msg
		}
		details = append(details, Detail{Pointer: ptr, Keyword: check, Message: msg, Sample: p.Sample, Field: p.Field})
		checks = append(checks, checkRow(check, msg, ptr, p))
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: CodePublishValidationFailed, Details: details,
		Message: fmt.Sprintf("%d publish check(s) failed", len(problems)),
		Meta:    map[string]any{"report": map[string]any{"valid": false, "checks": checks}}}
}

var index = regexp.MustCompile(`\[(\d+)\]`)

// pointer turns a registry path (targets[1].account) into a JSON pointer (/targets/1/account).
func pointer(path string) string {
	if path == "" {
		return ""
	}
	return "/" + strings.ReplaceAll(index.ReplaceAllString(path, ".$1"), ".", "/")
}

// checkRow is one failed ValidationCheck, with the sample and field of a sample check.
func checkRow(check, msg, ptr string, p registry.Problem) map[string]any {
	return map[string]any{"check": check, "status": "fail", "message": msg, "pointer": nullable(ptr),
		"sample": nullable(p.Sample), "field": nullable(p.Field)}
}

// checkOf names the DESIGN §6.4 check a problem failed (ValidationCheck.check): the one the
// registry named, else one derived from the message.
func checkOf(p registry.Problem) string {
	if p.Check != "" {
		return p.Check
	}
	m, path := strings.ToLower(p.Message), strings.ToLower(p.Path)
	switch {
	case strings.Contains(m, "is not an operation of"):
		return registry.CheckOperationsHaveTransformers // registry checkOperations (HUB-343)
	case strings.Contains(m, "golden"):
		return "goldens_pass"
	case strings.Contains(m, "retired"):
		return "accounts_not_retired"
	case strings.Contains(m, "weights must total"):
		return "weights_sum_100"
	case strings.Contains(m, "interactive"):
		return "interactive_sync_only"
	case strings.Contains(m, "polling: true or a webhook") || strings.Contains(m, "needs at least one webhook"):
		return "delivery_path_declared"
	case strings.Contains(m, "unknown"):
		return "references_exist"
	case strings.Contains(path, "transformer") || strings.Contains(m, "expression") || strings.Contains(m, "type"):
		return "expressions_typecheck"
	}
	return "schemas_compile"
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
