// Package auth is the process-local actor boundary.
// A bearer token is mapped to an actor and an owner by server configuration.
// The owner id is not a Makez workspace id, and a client field is not a credential.
package auth

import (
	"errors"
	"strings"
)

// ErrUnauthenticated means the request has no credential this process accepts.
var ErrUnauthenticated = errors.New("authentication required")

// Principal is the authenticated actor and the owner that actor may act for.
// The actor is an audit record. It is not part of the idempotency scope.
type Principal struct {
	ActorID string
	OwnerID string
}

// Credential is one server-configured bearer token.
type Credential struct {
	Token   string
	ActorID string
	OwnerID string
}

// Authenticator checks Authorization headers against the configured credentials.
type Authenticator struct {
	byToken map[string]Principal
}

// New builds an authenticator. An empty list accepts nobody.
func New(creds []Credential) Authenticator {
	byToken := make(map[string]Principal, len(creds))
	for _, cred := range creds {
		if cred.Token == "" || cred.ActorID == "" || cred.OwnerID == "" {
			continue
		}
		byToken[cred.Token] = Principal{ActorID: cred.ActorID, OwnerID: cred.OwnerID}
	}
	return Authenticator{byToken: byToken}
}

// Authenticate reads a Bearer token. The owner is the server binding of that
// token, never a value taken from the request body.
func (a Authenticator) Authenticate(header string) (Principal, error) {
	token, ok := bearerToken(header)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	principal, ok := a.byToken[token]
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

// ParseCredentials reads token|actor|owner entries separated by commas.
// An empty string configures no credentials. This is not a user directory.
func ParseCredentials(raw string) ([]Credential, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]Credential, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, "|")
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
			return nil, errors.New("PHILO_AUTH_CREDENTIALS entries must be token|actor|owner")
		}
		out = append(out, Credential{Token: fields[0], ActorID: fields[1], OwnerID: fields[2]})
	}
	if len(out) == 0 {
		return nil, errors.New("PHILO_AUTH_CREDENTIALS entries must be token|actor|owner")
	}
	return out, nil
}

func bearerToken(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if len(header) < len("Bearer ") || !strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[len("Bearer "):])
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}
