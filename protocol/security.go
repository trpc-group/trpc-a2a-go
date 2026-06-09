// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

// SecurityScheme represents an authentication scheme supported by the agent.
// Compatible with OpenAPI 3.0 SecurityScheme object.
type SecurityScheme struct {
	Type             SecuritySchemeType `json:"type"`
	Description      *string           `json:"description,omitempty"`
	Name             *string           `json:"name,omitempty"`
	In               *SecuritySchemeIn  `json:"in,omitempty"`
	Scheme           *string           `json:"scheme,omitempty"`
	BearerFormat     *string           `json:"bearerFormat,omitempty"`
	Flows            *OAuthFlows       `json:"flows,omitempty"`
	OpenIDConnectURL *string           `json:"openIdConnectUrl,omitempty"`
}

// SecuritySchemeType represents the type of security scheme.
type SecuritySchemeType string

const (
	SecuritySchemeTypeAPIKey         SecuritySchemeType = "apiKey"
	SecuritySchemeTypeHTTP           SecuritySchemeType = "http"
	SecuritySchemeTypeOAuth2         SecuritySchemeType = "oauth2"
	SecuritySchemeTypeOpenIDConnect  SecuritySchemeType = "openIdConnect"
)

// SecuritySchemeIn represents where to include the security credentials.
type SecuritySchemeIn string

const (
	SecuritySchemeInQuery  SecuritySchemeIn = "query"
	SecuritySchemeInHeader SecuritySchemeIn = "header"
	SecuritySchemeInCookie SecuritySchemeIn = "cookie"
)

// OAuthFlows represents OAuth2 flow configurations.
type OAuthFlows struct {
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty"`
	Implicit          *OAuthFlow `json:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty"`
}

// OAuthFlow represents a single OAuth2 flow configuration.
type OAuthFlow struct {
	AuthorizationURL *string           `json:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl"`
	RefreshURL       *string           `json:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes"`
}
