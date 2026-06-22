// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// rpcEcho spins up a server that asserts the JSON-RPC method and returns a
// canned result, returning the decoded request method for inspection.
func rpcServer(t *testing.T, wantMethod string, result interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpc.Request
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, wantMethod, req.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonrpc.NewResponse(req.ID, result))
	}))
}

func TestA2AClient_ListTasks(t *testing.T) {
	srv := rpcServer(t, protocol.MethodTasksList, &protocol.ListTasksResult{
		Tasks:     []*protocol.Task{{ID: "t1", Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}},
		TotalSize: 1,
		PageSize:  50,
	})
	defer srv.Close()
	c, err := NewA2AClient(srv.URL)
	require.NoError(t, err)

	res, err := c.ListTasks(context.Background(), protocol.ListTasksParams{ContextID: "c1"})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, "t1", res.Tasks[0].ID)
	assert.Equal(t, 1, res.TotalSize)
}

func TestA2AClient_ListPushNotifications(t *testing.T) {
	srv := rpcServer(t, protocol.MethodTasksPushNotificationConfigList,
		&protocol.ListTaskPushNotificationConfigsResult{
			Configs: []protocol.TaskPushNotificationConfig{{TaskID: "t1", URL: "https://x/hook"}},
		})
	defer srv.Close()
	c, err := NewA2AClient(srv.URL)
	require.NoError(t, err)

	res, err := c.ListPushNotifications(context.Background(),
		protocol.ListTaskPushNotificationConfigsParams{TaskID: "t1"})
	require.NoError(t, err)
	require.Len(t, res.Configs, 1)
	assert.Equal(t, "https://x/hook", res.Configs[0].URL)
}

func TestA2AClient_DeletePushNotification(t *testing.T) {
	srv := rpcServer(t, protocol.MethodTasksPushNotificationConfigDelete, struct{}{})
	defer srv.Close()
	c, err := NewA2AClient(srv.URL)
	require.NoError(t, err)

	err = c.DeletePushNotification(context.Background(),
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: "t1", ID: "cfg-1"})
	require.NoError(t, err)
}

func TestA2AClient_DeletePushNotification_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpc.Request
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonrpc.NewErrorResponse(req.ID, jsonrpc.ErrInvalidParams("bad")))
	}))
	defer srv.Close()
	c, err := NewA2AClient(srv.URL)
	require.NoError(t, err)

	err = c.DeletePushNotification(context.Background(),
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: "t1"})
	require.Error(t, err)
}
