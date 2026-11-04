package ytr

import (
	"context"
	"errors"
	"strings"
)

const (
	OAuth  AuthScheme = "OAuth"
	Bearer AuthScheme = "Bearer"
)

const (
	Organization360   OrganizationKind = "360"
	OrganizationCloud OrganizationKind = "cloud"
)

// AuthScheme is the token prefix accepted by Tracker.
type AuthScheme string

// Token is an access token. Never log its Value.
type Token struct {
	Scheme AuthScheme
	Value  string
}

// TokenSource supplies credentials for one request. Refresh and storage are
// application responsibilities. Implementations must support concurrent calls.
type TokenSource interface {
	Token(context.Context) (Token, error)
}

// TokenSourceFunc adapts a function to the TokenSource interface.
type TokenSourceFunc func(context.Context) (Token, error)

// OrganizationKind selects the Tracker organization header.
type OrganizationKind string

// Organization identifies a Yandex 360 or Yandex Cloud organization.
type Organization struct {
	Kind OrganizationKind
	ID   string
}

type staticToken struct{ value Token }

// OAuthToken returns an immutable source for an OAuth access token.
func OAuthToken(value string) TokenSource { return staticToken{Token{OAuth, value}} }

// IAMToken returns an immutable source for a Yandex Cloud IAM token.
func IAMToken(value string) TokenSource { return staticToken{Token{Bearer, value}} }

// Token calls f with the request context.
func (f TokenSourceFunc) Token(ctx context.Context) (Token, error) { return f(ctx) }

func (s staticToken) Token(context.Context) (Token, error) { return s.value, nil }

func (o Organization) header() (string, error) {
	if !validHeaderValue(o.ID) || strings.TrimSpace(o.ID) == "" {
		return "", errors.New("ytr: invalid organization ID")
	}

	switch o.Kind {
	case Organization360:
		return "X-Org-ID", nil
	case OrganizationCloud:
		return "X-Cloud-Org-ID", nil
	default:
		return "", errors.New("ytr: organization kind must be 360 or cloud")
	}
}

func authorization(token Token) (string, error) {
	if token.Scheme != OAuth && token.Scheme != Bearer {
		return "", errors.New("ytr: unsupported token scheme")
	}

	if token.Value == "" || strings.ContainsAny(token.Value, " \t\r\n") || !validHeaderValue(token.Value) {
		return "", errors.New("ytr: invalid access token")
	}

	return string(token.Scheme) + " " + token.Value, nil
}

func validHeaderValue(s string) bool {
	for _, v := range []byte(s) {
		if v < 32 || v == 127 {
			return false
		}
	}

	return true
}
