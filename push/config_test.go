// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package push

import (
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestValidateConfig(t *testing.T) {
	valid := protocol.TaskPushNotificationConfig{TaskID: "task-1", URL: "https://webhook.example/notify"}
	tests := []struct {
		name string
		edit func(*protocol.TaskPushNotificationConfig)
		want bool
	}{
		{name: "valid", want: true},
		{name: "http allowed", edit: func(c *protocol.TaskPushNotificationConfig) { c.URL = "http://webhook.example" }, want: true},
		{name: "missing task", edit: func(c *protocol.TaskPushNotificationConfig) { c.TaskID = "" }},
		{name: "missing URL", edit: func(c *protocol.TaskPushNotificationConfig) { c.URL = "" }},
		{name: "unsupported scheme", edit: func(c *protocol.TaskPushNotificationConfig) { c.URL = "file:///tmp/hook" }},
		{name: "missing host", edit: func(c *protocol.TaskPushNotificationConfig) { c.URL = "https:///hook" }},
		{name: "URL user info", edit: func(c *protocol.TaskPushNotificationConfig) { c.URL = "https://user:secret@webhook.example" }},
		{name: "missing auth scheme", edit: func(c *protocol.TaskPushNotificationConfig) {
			c.Authentication = &protocol.AuthenticationInfo{Credentials: "token"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			if tt.edit != nil {
				tt.edit(&cfg)
			}
			if got := ValidateConfig(cfg); (got == nil) != tt.want {
				t.Fatalf("ValidateConfig() error = %v, success want %v", got, tt.want)
			}
		})
	}
}
