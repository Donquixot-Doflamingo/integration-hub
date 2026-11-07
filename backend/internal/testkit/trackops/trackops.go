// Package trackops holds the operations block of the dev events-tracking integrations
// (backend/config/integrations/int_evt_*/versions, ADR-0023) for test overlays that write their
// own integration versions: every version declares its operations and request schemas.
package trackops

// Block is the YAML of the "operations:" key (default operation, events-tracking request
// schema), ending with a newline. Insert it as a top-level key of a version document.
const Block = `operations:
  default:
    description: Track a single event for a user.
    idempotent: true
    request_schema:
      body:
        type: object
        required: [user, event]
        additionalProperties: false
        properties:
          user:
            type: object
            required: [id]
            additionalProperties: false
            properties:
              id: { type: string, minLength: 1, maxLength: 256, x-pii: identifier, description: Stable identifier of the end user in the caller's system. }
          event:
            type: object
            required: [name]
            additionalProperties: false
            properties:
              name: { type: string, minLength: 1, maxLength: 128, pattern: '^[A-Za-z][A-Za-z0-9_.:-]*$' }
              timestamp: { type: string, format: date-time, description: When the event happened (RFC 3339). Defaults to the Hub receive time. }
              properties: { type: object, maxProperties: 256, x-pii: free_text, description: Free-form event properties. }
          context:
            type: object
            additionalProperties: false
            properties:
              campaign: { type: string, maxLength: 128 }
`

// Member is Block as a JSON member of the admin contract's IntegrationSpec ("operations": {...},
// without a trailing comma).
const Member = `"operations":{"default":{"description":"Track a single event for a user.","idempotent":true,"request_schema":{"body":{"additionalProperties":false,"properties":{"context":{"additionalProperties":false,"properties":{"campaign":{"maxLength":128,"type":"string"}},"type":"object"},"event":{"additionalProperties":false,"properties":{"name":{"maxLength":128,"minLength":1,"pattern":"^[A-Za-z][A-Za-z0-9_.:-]*$","type":"string"},"properties":{"description":"Free-form event properties.","maxProperties":256,"type":"object","x-pii":"free_text"},"timestamp":{"description":"When the event happened (RFC 3339). Defaults to the Hub receive time.","format":"date-time","type":"string"}},"required":["name"],"type":"object"},"user":{"additionalProperties":false,"properties":{"id":{"description":"Stable identifier of the end user in the caller's system.","maxLength":256,"minLength":1,"type":"string","x-pii":"identifier"}},"required":["id"],"type":"object"}},"required":["user","event"],"type":"object"}}}}`
