package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Body limits (DESIGN §16.5): 1 MB. The 4 MB LLM limit was keyed on llm.* capability names,
// which ADR-0023 removed; it returns with an explicit integration setting (HUB-439, ADR-0024).
const (
	DefaultBodyLimit = 1 << 20
	maxDetails       = registry.MaxViolations // contract: details maxItems
)

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

// validate checks the caller's input against the operation's pre-compiled request schema
// (DESIGN §5.1 step 2, ADR-0023): the body, then the declared caller headers. It lists every
// violation: body ones with their JSON pointer into the body, header ones without a pointer and
// with the header named in the message (contract CanonicalError.details). Only operations with
// validate_input on are checked (the caller decides with Operation.Validates).
func validate(op *registry.Operation, body map[string]any, hdr http.Header) *core.CanonicalError {
	var details []core.ErrorDetail
	for _, v := range registry.Violations(op.RequestSchema.Body.Compiled(), body, "") {
		details = append(details, core.ErrorDetail{Pointer: v.Field, Keyword: v.Keyword, Message: v.Message})
	}
	if h := op.RequestSchema.Headers.Compiled(); h != nil && len(details) < maxDetails {
		declared := make(map[string]any, len(op.HeaderNames()))
		for _, name := range op.HeaderNames() {
			if v := hdr.Get(name); v != "" {
				declared[name] = v
			}
		}
		for _, v := range registry.Violations(h, declared, "") {
			name := strings.TrimPrefix(v.Field, "/")
			details = append(details, core.ErrorDetail{Keyword: v.Keyword, Message: "header " + name + ": " + v.Message})
		}
	}
	if len(details) == 0 {
		return nil
	}
	if len(details) > maxDetails {
		details = details[:maxDetails]
	}
	return &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Request does not match the operation's request_schema",
		Reason: core.Ptr("schema"), Details: details}
}
