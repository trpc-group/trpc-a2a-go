// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package push

import (
	"fmt"
	"net/url"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// ValidateConfig validates protocol-level push configuration fields. Network
// destination policy is enforced again by HTTPSender at delivery time, where
// DNS resolution cannot become stale between registration and use.
func ValidateConfig(cfg protocol.TaskPushNotificationConfig) error {
	if cfg.TaskID == "" {
		return fmt.Errorf("task ID is required")
	}
	if cfg.URL == "" {
		return fmt.Errorf("push notification URL is required")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("push notification URL must be a valid http(s) URL")
	}
	if u.User != nil {
		return fmt.Errorf("push notification URL must not contain user information")
	}
	if cfg.Authentication != nil && strings.TrimSpace(cfg.Authentication.Scheme) == "" {
		return fmt.Errorf("push notification authentication scheme is required")
	}
	return nil
}
