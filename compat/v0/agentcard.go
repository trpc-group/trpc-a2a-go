// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// FillLegacyCardFields populates the deprecated v0.x AgentCard fields from
// their v1.0 counterparts so that legacy clients can parse the card. Combined
// with protocol.AgentCard.NormalizeInterfaces (which fills the v1 fields from
// the legacy ones), this produces a dual-format card readable by both
// protocol generations.
//
// Fields already set are left untouched.
func FillLegacyCardFields(card *protocol.AgentCard) {
	if card == nil {
		return
	}
	// Mirror v1.0 security requirements into the deprecated v0 "security" key.
	card.NormalizeSecurity()
	if len(card.SupportedInterfaces) > 0 {
		preferred := card.SupportedInterfaces[0]
		if card.URL == "" {
			card.URL = preferred.URL
		}
		if card.PreferredTransport == nil && preferred.ProtocolBinding != "" {
			binding := preferred.ProtocolBinding
			card.PreferredTransport = &binding
		}
		if card.AdditionalInterfaces == nil && len(card.SupportedInterfaces) > 1 {
			card.AdditionalInterfaces = card.SupportedInterfaces[1:]
		}
	}
	if card.ProtocolVersion == nil {
		// Advertise the legacy protocol version this compat layer emulates;
		// the per-interface protocolVersion fields carry the v1.0 value.
		version := Version
		card.ProtocolVersion = &version
	}
	if card.SupportsAuthenticatedExtendedCard == nil && card.Capabilities.ExtendedAgentCard != nil {
		enabled := *card.Capabilities.ExtendedAgentCard
		card.SupportsAuthenticatedExtendedCard = &enabled
	}
}
