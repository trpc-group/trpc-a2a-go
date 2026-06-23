// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// fetchCardName GETs the agent-card endpoint (optionally with ?tenant=) and
// returns the HTTP status and the served card's Name.
func fetchCardName(t *testing.T, base, tenant string) (int, string) {
	t.Helper()
	url := base + protocol.AgentCardPath
	if tenant != "" {
		url += "?tenant=" + tenant
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	return resp.StatusCode, card.Name
}

// TestTenantCard_StaticRegistry verifies WithTenantCard: the agent-card endpoint
// serves the right card per "?tenant=", the default card with no tenant, and 404
// for an unknown tenant on a multi-tenant server.
func TestTenantCard_StaticRegistry(t *testing.T) {
	def := AgentCard{Name: "Default", URL: "http://localhost"}
	srv, err := NewA2AServer(newMockTaskManager(), WithAgentCard(def),
		WithTenantCard("chatAgent", AgentCard{Name: "ChatAgent"}),
		WithTenantCard("workerAgent", AgentCard{Name: "WorkerAgent"}),
	)
	if err != nil {
		t.Fatalf("NewA2AServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, name := fetchCardName(t, ts.URL, "chatAgent"); code != 200 || name != "ChatAgent" {
		t.Errorf("tenant=chatAgent: code=%d name=%q", code, name)
	}
	if code, name := fetchCardName(t, ts.URL, "workerAgent"); code != 200 || name != "WorkerAgent" {
		t.Errorf("tenant=workerAgent: code=%d name=%q", code, name)
	}
	if code, name := fetchCardName(t, ts.URL, ""); code != 200 || name != "Default" {
		t.Errorf("no tenant: code=%d name=%q, want Default", code, name)
	}
	if code, _ := fetchCardName(t, ts.URL, "ghost"); code != http.StatusNotFound {
		t.Errorf("unknown tenant: code=%d, want 404", code)
	}
}

// TestTenantCard_DynamicProvider verifies WithTenantCardProvider resolves tenants
// not known at startup, and 404s on an unknown one.
func TestTenantCard_DynamicProvider(t *testing.T) {
	srv, err := NewA2AServer(newMockTaskManager(),
		WithAgentCard(AgentCard{Name: "Default", URL: "http://localhost"}),
		WithTenantCardProvider(func(_ context.Context, tenant string) (AgentCard, error) {
			if tenant == "dyn-42" {
				return AgentCard{Name: "Dynamic-" + tenant}, nil
			}
			return AgentCard{}, errors.New("unknown tenant")
		}),
	)
	if err != nil {
		t.Fatalf("NewA2AServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, name := fetchCardName(t, ts.URL, "dyn-42"); code != 200 || name != "Dynamic-dyn-42" {
		t.Errorf("dynamic tenant: code=%d name=%q", code, name)
	}
	if code, _ := fetchCardName(t, ts.URL, "nope"); code != http.StatusNotFound {
		t.Errorf("unknown dynamic tenant: code=%d, want 404", code)
	}
}
