// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"bytes"
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
// The JSON wire form is the ProtoJSON encoding of
// SecurityRequirement.schemes (map<string, StringList>): each entry is
// {"schemes": {"name": {"list": ["scope"]}}}. UnmarshalJSON also accepts the
// scopes as a bare array — the shape earlier v2 prereleases emitted — and the
// v0 flat form ({"name": ["scope"]}). The deprecated v0 "security" key is
// mirrored separately on AgentCard (see AgentCard.Security and
// NormalizeSecurity).
type SecurityRequirements []map[string][]string

// stringListWire is the ProtoJSON form of the StringList message that wraps a
// requirement's scopes. It also decodes a bare array so cards written by
// earlier v2 prereleases still parse.
type stringListWire struct {
	List []string `json:"list"`
}

func (l stringListWire) MarshalJSON() ([]byte, error) {
	list := l.List
	if list == nil {
		list = []string{}
	}
	return json.Marshal(struct {
		List []string `json:"list"`
	}{List: list})
}

func (l *stringListWire) UnmarshalJSON(data []byte) error {
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped != nil {
		if len(wrapped) == 0 {
			l.List = nil
			return nil
		}
		list, ok := wrapped["list"]
		if !ok {
			// StringList.list is not required. After discarding unknown fields,
			// an object without it is equivalent to an empty StringList.
			l.List = nil
			return nil
		}
		if err := json.Unmarshal(list, &l.List); err != nil {
			return fmt.Errorf("invalid security requirement scopes: %w", err)
		}
		return nil
	}
	var bare []string
	if err := json.Unmarshal(data, &bare); err != nil {
		return fmt.Errorf("invalid security requirement scopes: %w", err)
	}
	l.List = bare
	return nil
}

type securityRequirementWire struct {
	Schemes map[string]stringListWire `json:"schemes"`
}

// MarshalJSON emits the v1.0 wrapped form: [{"schemes": {"name": {"list": [...]}}}, ...].
func (r SecurityRequirements) MarshalJSON() ([]byte, error) {
	out := make([]securityRequirementWire, 0, len(r))
	for _, req := range r {
		schemes := make(map[string]stringListWire, len(req))
		for name, scopes := range req {
			schemes[name] = stringListWire{List: scopes}
		}
		out = append(out, securityRequirementWire{Schemes: schemes})
	}
	return json.Marshal(out)
}

// UnmarshalJSON accepts the v1.0 wrapped form, the wrapped form with bare
// arrays emitted by earlier v2 prereleases, and the v0 flat form. Each entry
// is decoded independently so a mixed compatibility document cannot silently
// lose one of its security alternatives.
func (r *SecurityRequirements) UnmarshalJSON(data []byte) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("invalid security requirements: %w", err)
	}
	result := make(SecurityRequirements, 0, len(entries))
	for i, entry := range entries {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(entry, &object); err != nil || object == nil {
			return fmt.Errorf("invalid security requirement at index %d", i)
		}
		if schemesJSON, ok := object["schemes"]; ok {
			trimmed := bytes.TrimSpace(schemesJSON)
			var wrappedObject map[string]json.RawMessage
			if string(trimmed) == "null" {
				result = append(result, map[string][]string{})
				continue
			}
			if err := json.Unmarshal(schemesJSON, &wrappedObject); err == nil && wrappedObject != nil {
				var wire map[string]stringListWire
				if err := json.Unmarshal(schemesJSON, &wire); err != nil {
					return fmt.Errorf("invalid security requirement at index %d: %w", i, err)
				}
				schemes := make(map[string][]string, len(wire))
				for name, scopes := range wire {
					schemes[name] = scopes.List
				}
				result = append(result, schemes)
				continue
			}
			if len(trimmed) == 0 || trimmed[0] != '[' {
				return fmt.Errorf("invalid security requirement at index %d", i)
			}
		}
		flat := make(map[string][]string)
		for name, scopesJSON := range object {
			var scopes []string
			if err := json.Unmarshal(scopesJSON, &scopes); err != nil {
				// Unknown v1 fields are discarded. Array-valued properties remain
				// accepted as the legacy flat security-requirement form.
				continue
			}
			flat[name] = scopes
		}
		result = append(result, flat)
	}
	*r = result
	return nil
}
