// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"fmt"
)

// SecurityScheme describes an authentication scheme an agent supports.
//
// The user-facing fields keep the ergonomic OpenAPI-style (v0.x) shape, but the
// JSON wire form is a SUPERSET: it emits both the v0 flat fields
// ({"type":"apiKey","in":...,"name":...}) and the v1.0 discriminated-union
// wrapper key ({"apiKeySecurityScheme":{"location":...,"name":...}}). A v0
// client reads the flat fields; a v1.0 client reads the wrapper and ignores the
// rest. UnmarshalJSON accepts either shape.
type SecurityScheme struct {
	Type             SecuritySchemeType `json:"type"`
	Description      *string            `json:"description,omitempty"`
	Name             *string            `json:"name,omitempty"`
	In               *SecuritySchemeIn  `json:"in,omitempty"`
	Scheme           *string            `json:"scheme,omitempty"`
	BearerFormat     *string            `json:"bearerFormat,omitempty"`
	Flows            *OAuthFlows        `json:"flows,omitempty"`
	OpenIDConnectURL *string            `json:"openIdConnectUrl,omitempty"`
	// OAuth2MetadataURL is the v1.0 OAuth2 authorization-server metadata URL
	// (RFC 8414). It applies to the OAuth2 scheme.
	OAuth2MetadataURL *string `json:"oauth2MetadataUrl,omitempty"`
}

// SecuritySchemeType represents the type of security scheme.
type SecuritySchemeType string

// SecuritySchemeType constants enumerate the supported security-scheme types.
const (
	SecuritySchemeTypeAPIKey        SecuritySchemeType = "apiKey"
	SecuritySchemeTypeHTTP          SecuritySchemeType = "http"
	SecuritySchemeTypeOAuth2        SecuritySchemeType = "oauth2"
	SecuritySchemeTypeOpenIDConnect SecuritySchemeType = "openIdConnect"
	// SecuritySchemeTypeMutualTLS is the v1.0 mutual-TLS scheme.
	SecuritySchemeTypeMutualTLS SecuritySchemeType = "mutualTLS"
)

// SecuritySchemeIn represents where to include the security credentials.
type SecuritySchemeIn string

// SecuritySchemeIn constants enumerate the API-key locations.
const (
	SecuritySchemeInQuery  SecuritySchemeIn = "query"
	SecuritySchemeInHeader SecuritySchemeIn = "header"
	SecuritySchemeInCookie SecuritySchemeIn = "cookie"
)

// OAuthFlows represents OAuth2 flow configurations. The JSON keys
// (authorizationCode/clientCredentials/implicit/password/deviceCode) match both
// v0 and v1.0; v1.0 deprecated implicit/password and added deviceCode.
type OAuthFlows struct {
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty"`
	Implicit          *OAuthFlow `json:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty"`
	DeviceCode        *OAuthFlow `json:"deviceCode,omitempty"`
}

// OAuthFlow represents a single OAuth2 flow configuration.
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty"`
	// PKCERequired is the v1.0 RFC 7636 hint for the authorization-code flow.
	PKCERequired bool `json:"pkceRequired,omitempty"`
	// DeviceAuthorizationURL is the device-authorization endpoint for the
	// v1.0 device-code flow.
	DeviceAuthorizationURL string `json:"deviceAuthorizationUrl,omitempty"`
}

// ---------------------------------------------------------------------------
// Dual-format (v0 flat + v1.0 oneof wrapper) JSON serialization
// ---------------------------------------------------------------------------

type apiKeyWire struct {
	Description *string `json:"description,omitempty"`
	Location    string  `json:"location,omitempty"`
	Name        *string `json:"name,omitempty"`
}

type httpAuthWire struct {
	Description  *string `json:"description,omitempty"`
	Scheme       *string `json:"scheme,omitempty"`
	BearerFormat *string `json:"bearerFormat,omitempty"`
}

type oauth2Wire struct {
	Description       *string     `json:"description,omitempty"`
	Flows             *OAuthFlows `json:"flows,omitempty"`
	OAuth2MetadataURL *string     `json:"oauth2MetadataUrl,omitempty"`
}

type openIDConnectWire struct {
	Description      *string `json:"description,omitempty"`
	OpenIDConnectURL *string `json:"openIdConnectUrl,omitempty"`
}

type mtlsWire struct {
	Description *string `json:"description,omitempty"`
}

// securitySchemeWire is the union of the v0 flat fields and the v1.0 oneof
// wrapper keys, used to produce and parse the superset wire form.
type securitySchemeWire struct {
	// v0 flat fields.
	Type             SecuritySchemeType `json:"type,omitempty"`
	Description      *string            `json:"description,omitempty"`
	Name             *string            `json:"name,omitempty"`
	In               *SecuritySchemeIn  `json:"in,omitempty"`
	Scheme           *string            `json:"scheme,omitempty"`
	BearerFormat     *string            `json:"bearerFormat,omitempty"`
	Flows            *OAuthFlows        `json:"flows,omitempty"`
	OpenIDConnectURL *string            `json:"openIdConnectUrl,omitempty"`

	// v1.0 oneof wrapper keys.
	APIKey        *apiKeyWire        `json:"apiKeySecurityScheme,omitempty"`
	HTTPAuth      *httpAuthWire      `json:"httpAuthSecurityScheme,omitempty"`
	OAuth2        *oauth2Wire        `json:"oauth2SecurityScheme,omitempty"`
	OpenIDConnect *openIDConnectWire `json:"openIdConnectSecurityScheme,omitempty"`
	MutualTLS     *mtlsWire          `json:"mtlsSecurityScheme,omitempty"`
}

// MarshalJSON emits the superset (v0 flat fields + v1.0 oneof wrapper key).
func (s SecurityScheme) MarshalJSON() ([]byte, error) {
	w := securitySchemeWire{
		Type:             s.Type,
		Description:      s.Description,
		Name:             s.Name,
		In:               s.In,
		Scheme:           s.Scheme,
		BearerFormat:     s.BearerFormat,
		Flows:            s.Flows,
		OpenIDConnectURL: s.OpenIDConnectURL,
	}
	switch s.Type {
	case SecuritySchemeTypeAPIKey:
		var loc string
		if s.In != nil {
			loc = string(*s.In)
		}
		w.APIKey = &apiKeyWire{Description: s.Description, Location: loc, Name: s.Name}
	case SecuritySchemeTypeHTTP:
		w.HTTPAuth = &httpAuthWire{Description: s.Description, Scheme: s.Scheme, BearerFormat: s.BearerFormat}
	case SecuritySchemeTypeOAuth2:
		w.OAuth2 = &oauth2Wire{Description: s.Description, Flows: s.Flows, OAuth2MetadataURL: s.OAuth2MetadataURL}
	case SecuritySchemeTypeOpenIDConnect:
		w.OpenIDConnect = &openIDConnectWire{Description: s.Description, OpenIDConnectURL: s.OpenIDConnectURL}
	case SecuritySchemeTypeMutualTLS:
		w.MutualTLS = &mtlsWire{Description: s.Description}
	}
	return json.Marshal(w)
}

// UnmarshalJSON accepts both the v1.0 oneof wrapper and the v0 flat shape.
func (s *SecurityScheme) UnmarshalJSON(data []byte) error {
	var w securitySchemeWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	switch {
	case w.APIKey != nil:
		s.Type = SecuritySchemeTypeAPIKey
		s.Description = w.APIKey.Description
		s.Name = w.APIKey.Name
		if w.APIKey.Location != "" {
			in := SecuritySchemeIn(w.APIKey.Location)
			s.In = &in
		}
	case w.HTTPAuth != nil:
		s.Type = SecuritySchemeTypeHTTP
		s.Description = w.HTTPAuth.Description
		s.Scheme = w.HTTPAuth.Scheme
		s.BearerFormat = w.HTTPAuth.BearerFormat
	case w.OAuth2 != nil:
		s.Type = SecuritySchemeTypeOAuth2
		s.Description = w.OAuth2.Description
		s.Flows = w.OAuth2.Flows
		s.OAuth2MetadataURL = w.OAuth2.OAuth2MetadataURL
	case w.OpenIDConnect != nil:
		s.Type = SecuritySchemeTypeOpenIDConnect
		s.Description = w.OpenIDConnect.Description
		s.OpenIDConnectURL = w.OpenIDConnect.OpenIDConnectURL
	case w.MutualTLS != nil:
		s.Type = SecuritySchemeTypeMutualTLS
		s.Description = w.MutualTLS.Description
	default:
		// v0 flat shape.
		s.Type = w.Type
		s.Description = w.Description
		s.Name = w.Name
		s.In = w.In
		s.Scheme = w.Scheme
		s.BearerFormat = w.BearerFormat
		s.Flows = w.Flows
		s.OpenIDConnectURL = w.OpenIDConnectURL
	}
	return nil
}

// ---------------------------------------------------------------------------
// SecurityRequirements
// ---------------------------------------------------------------------------

// SecurityRequirements is a list of alternative security requirement sets. Each
// entry maps a security-scheme name to the required scopes.
//
// The JSON wire form follows v1.0: each entry is wrapped as
// {"schemes": {"name": ["scope"]}}. UnmarshalJSON also accepts the v0 flat form
// ({"name": ["scope"]}). The deprecated v0 "security" key is mirrored separately
// on AgentCard (see AgentCard.Security and NormalizeSecurity).
type SecurityRequirements []map[string][]string

type securityRequirementWire struct {
	Schemes map[string][]string `json:"schemes"`
}

// MarshalJSON emits the v1.0 wrapped form: [{"schemes": {...}}, ...].
func (r SecurityRequirements) MarshalJSON() ([]byte, error) {
	out := make([]securityRequirementWire, 0, len(r))
	for _, req := range r {
		out = append(out, securityRequirementWire{Schemes: req})
	}
	return json.Marshal(out)
}

// UnmarshalJSON accepts both the v1.0 wrapped form and the v0 flat form.
func (r *SecurityRequirements) UnmarshalJSON(data []byte) error {
	var wrapped []securityRequirementWire
	if err := json.Unmarshal(data, &wrapped); err == nil && hasSchemesKey(wrapped) {
		result := make(SecurityRequirements, 0, len(wrapped))
		for _, w := range wrapped {
			result = append(result, w.Schemes)
		}
		*r = result
		return nil
	}
	// Fall back to the v0 flat form.
	var flat []map[string][]string
	if err := json.Unmarshal(data, &flat); err != nil {
		return fmt.Errorf("invalid security requirements: %w", err)
	}
	*r = flat
	return nil
}

// hasSchemesKey reports whether at least one entry decoded a non-nil schemes
// map, distinguishing the wrapped form from the flat form (whose objects have
// no "schemes" key and therefore decode to a nil map).
func hasSchemesKey(wrapped []securityRequirementWire) bool {
	for _, w := range wrapped {
		if w.Schemes != nil {
			return true
		}
	}
	return len(wrapped) == 0
}
