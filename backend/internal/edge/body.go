package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Body limits (DESIGN §16.5): 1 MB, 4 MB for LLM capabilities.
const (
	DefaultBodyLimit = 1 << 20
	LLMBodyLimit     = 4 << 20
	maxDetails       = 100 // contract: details maxItems
)

// bodyLimit returns the limit for a capability key ("llm.chat@v1" → 4 MB).
func bodyLimit(capability string) int64 {
	if strings.HasPrefix(capability, "llm.") {
		return LLMBodyLimit
	}
	return DefaultBodyLimit
}

// readJSONBody reads and decodes the request body as a JSON object. An oversized body is refused
// from Content-Length before reading, and capped while reading otherwise (PAYLOAD_TOO_LARGE).
func readJSONBody(r *http.Request, limit int64) (map[string]any, []byte, *core.CanonicalError) {
	tooLarge := &core.CanonicalError{Code: core.CodePayloadTooLarge, Message: fmt.Sprintf("Body is larger than %d bytes", limit)}
	if r.ContentLength > limit {
		return nil, nil, tooLarge
	}
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, nil, tooLarge
		}
		return nil, nil, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Could not read the request body"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var body map[string]any
	if err := dec.Decode(&body); err != nil || dec.More() {
		return nil, nil, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Body must be one JSON object",
			Reason: core.Ptr("schema"), Details: []core.ErrorDetail{{Pointer: "", Message: "invalid JSON"}}}
	}
	if body == nil {
		return nil, nil, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Body must be a JSON object", Reason: core.Ptr("schema")}
	}
	return body, raw, nil
}

// validate checks body against the operation's pre-compiled request schema (DESIGN §5.1 step 2)
// and lists every violation with its JSON pointer.
func validate(op *registry.Operation, body map[string]any) *core.CanonicalError {
	err := op.Request.Validate(body)
	if err == nil {
		return nil
	}
	ce := &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Body does not match the capability schema", Reason: core.Ptr("schema")}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		ce.Details = []core.ErrorDetail{{Message: err.Error()}}
		return ce
	}
	seen := map[string]bool{}
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		msg := u.Error.String()
		if strings.HasPrefix(msg, "validation failed") || strings.HasPrefix(msg, "allOf failed") {
			continue // group wrappers: the leaves carry the detail
		}
		key := u.InstanceLocation + "|" + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		ce.Details = append(ce.Details, core.ErrorDetail{Pointer: u.InstanceLocation, Keyword: path.Base(u.KeywordLocation), Message: msg})
		if len(ce.Details) == maxDetails {
			break
		}
	}
	return ce
}
