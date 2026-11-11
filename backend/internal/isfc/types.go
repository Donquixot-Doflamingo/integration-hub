package isfc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Statuses used by ISFC tenants, clients and API keys.
const (
	StatusActive   = "ACTIVE"
	StatusInactive = "INACTIVE"
)

// Tenant is an ISFC tenant (GET /api/v1/tenant).
type Tenant struct {
	ID        string    `json:"id"` // UUID: the link
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ClientInfo is the part of a client's metadata the Hub reads (GET /api/v1/client/internal/{id}):
// the tenants linked to a panel, ACTIVE ones only and in panel order.
type ClientInfo struct {
	Status     string
	ClientType string
	Tenancy    string
	Tenants    []string // ISFC tenant UUIDs
}

// CallerKey is an API key a client issued to a caller client. Only the hash is kept.
type CallerKey struct {
	ID          string
	Name        string
	ClientID    string // the caller client the key was issued to ("allowed_client_id")
	KeyHash     string // registry.HashKey of the plaintext key
	Status      string
	Roles       []string
	Permissions []string
	UpdatedAt   time.Time
}

// ClientConfiguration is GET /api/v1/client/configuration/{id}, reduced to what the Hub uses.
type ClientConfiguration struct {
	Name   string
	Status string
	Keys   []CallerKey
}

// wireKey decodes one allowed_api_keys entry. The plaintext is hashed here and dropped. When
// ISFC adds key_sha256 (ISFC-CR-4) the Hub uses it and never receives plaintext at all.
type wireKey struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Key             string    `json:"key"`
	KeySHA256       string    `json:"key_sha256"`
	AllowedClientID string    `json:"allowed_client_id"`
	Status          string    `json:"status"`
	Roles           []string  `json:"roles"`
	Permissions     []string  `json:"permissions"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (w wireKey) callerKey() (CallerKey, error) {
	k := CallerKey{ID: w.ID, Name: w.Name, ClientID: w.AllowedClientID, Status: strings.ToUpper(w.Status),
		Roles: w.Roles, Permissions: w.Permissions, UpdatedAt: w.UpdatedAt}
	switch {
	case w.KeySHA256 != "":
		k.KeyHash = "sha256:" + strings.TrimPrefix(strings.ToLower(w.KeySHA256), "sha256:")
	case w.Key != "":
		k.KeyHash = registry.HashKey(w.Key)
	default:
		return CallerKey{}, fmt.Errorf("isfc: api key %q has no key value", w.ID)
	}
	return k, nil
}

// UnmarshalJSON hashes keys while decoding, so a ClientConfiguration never holds plaintext.
func (c *ClientConfiguration) UnmarshalJSON(b []byte) error {
	var w struct {
		Client struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"client"`
		AllowedAPIKeys []wireKey `json:"allowed_api_keys"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	c.Name, c.Status, c.Keys = w.Client.Name, w.Client.Status, nil
	for _, wk := range w.AllowedAPIKeys {
		k, err := wk.callerKey()
		if err != nil {
			return err
		}
		c.Keys = append(c.Keys, k)
	}
	return nil
}
