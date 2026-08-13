// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main is an interactive chat client for the basic A2A example server.
// It keeps a contextID across turns so the server can attach conversation history,
// and supports async task send / get / subscribe / cancel commands.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/auth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// longTaskMarker must match examples/basic/server.
// Sent by /long-task to simulate a long-running server turn (async/subscribe/cancel).
const longTaskMarker = "__long_task__"

var (
	host     = flag.String("host", "localhost:8080", "server address")
	stream   = flag.Bool("stream", false, "use message/stream instead of message/send")
	httpJSON = flag.Bool("http-json", true, "use HTTP+JSON/REST binding (false = JSON-RPC)")
	user     = flag.String("user", "alice", "Basic Auth username (alice or bob)")
	password = flag.String("password", "alice-pass", "Basic Auth password matching examples/basic/server")
)

func main() {
	flag.Parse()

	opts := []client.Option{
		// NOTE: http.Client.Timeout caps the WHOLE response body read, which
		// for message/stream / SubscribeToTask is the entire SSE lifetime.
		// 60s covers the 30s /long-task demo with some headroom.
		client.WithTimeout(60 * time.Second),
		client.WithAuthProvider(&basicAuthClientProvider{user: *user, password: *password}),
	}
	binding := "JSON-RPC"
	if *httpJSON {
		opts = append(opts, client.WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
		binding = "HTTP+JSON"
	}

	a2aClient, err := client.NewA2AClient(fmt.Sprintf("http://%s/", *host), opts...)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	mode := "message/send"
	if *stream {
		mode = "message/stream"
	}
	fmt.Printf("Connected to http://%s/ (%s, %s, user=%s)\n", *host, binding, mode, *user)
	printHelp()

	s := &session{client: a2aClient, stream: *stream}
	if err := s.runREPL(bufio.NewScanner(os.Stdin)); err != nil {
		log.Fatalf("Failed to read stdin: %v", err)
	}
}

// session holds REPL state across turns.
type session struct {
	client     *client.A2AClient
	stream     bool
	contextID  *string
	lastTaskID string
}

// basicAuthClientProvider attaches HTTP Basic Auth to every client request.
type basicAuthClientProvider struct {
	user     string
	password string
}

func (p *basicAuthClientProvider) Authenticate(*http.Request) (*auth.User, error) {
	return &auth.User{ID: p.user}, nil
}

func (p *basicAuthClientProvider) ConfigureClient(c *http.Client) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cloned := *c
	cloned.Transport = &basicAuthTransport{base: base, user: p.user, password: p.password}
	return &cloned
}

type basicAuthTransport struct {
	base     http.RoundTripper
	user     string
	password string
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.SetBasicAuth(t.user, t.password)
	return t.base.RoundTrip(cloned)
}

func (s *session) runREPL(scanner *bufio.Scanner) error {
	for {
		fmt.Print("You> ")
		if !scanner.Scan() {
			fmt.Println()
			return scanner.Err()
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if s.handleLine(input) {
			return nil
		}
	}
}

// handleLine processes one REPL line. Returns true when the session should exit.
func (s *session) handleLine(input string) (quit bool) {
	if strings.EqualFold(input, "help") || strings.EqualFold(input, "/help") {
		printHelp()
		return false
	}
	if cmd, arg, ok := splitCommand(input); ok {
		return s.handleCommand(cmd, arg)
	}
	s.sendText(input)
	return false
}

func (s *session) handleCommand(cmd, arg string) (quit bool) {
	switch cmd {
	case "/quit", "/exit":
		fmt.Println("Bye.")
		return true
	case "/new":
		s.contextID = nil
		fmt.Println("(new conversation)")
	case "/long-task", "/async-long-task":
		s.runLongTask(cmd, arg)
	case "/gettask":
		s.withTaskID(arg, getTask)
	case "/subscribe":
		s.withTaskID(arg, subscribeTask)
	case "/cancel":
		s.withTaskID(arg, cancelTask)
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
	}
	return false
}

func (s *session) runLongTask(cmd, arg string) {
	// Simulate a long-running task (/long-task also auto-subscribes).
	if arg != "" {
		fmt.Printf("Note: %s takes no argument; extra text is ignored\n", cmd)
	}
	taskID, nextContextID, err := chatAsync(s.client, longTaskMarker, s.contextID)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	s.applyResult(taskID, nextContextID)
	if taskID == "" {
		return
	}
	if cmd == "/async-long-task" {
		fmt.Println("Tip: /subscribe to watch chunks, /gettask for snapshot, /cancel to stop")
		return
	}
	if err := subscribeTask(s.client, taskID); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}

func (s *session) withTaskID(arg string, fn func(*client.A2AClient, string) error) {
	taskID, err := resolveTaskID(arg, s.lastTaskID)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := fn(s.client, taskID); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}

func (s *session) sendText(input string) {
	msg := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(input)},
		nil,
		s.contextID,
	)
	var (
		taskID        string
		nextContextID *string
		err           error
	)
	if s.stream {
		taskID, nextContextID, err = chatStream(s.client, msg)
	} else {
		taskID, nextContextID, err = chatSend(s.client, msg)
	}
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	s.applyResult(taskID, nextContextID)
}

func (s *session) applyResult(taskID string, nextContextID *string) {
	if taskID != "" {
		s.lastTaskID = taskID
	}
	if nextContextID != nil {
		s.contextID = nextContextID
	}
}

func printHelp() {
	fmt.Println("Commands:")
	fmt.Println("  <text>              send a message (blocking or streaming mode)")
	fmt.Println("  /long-task          long-running demo (async + auto-subscribe)")
	fmt.Println("  /async-long-task    long-running demo (returnImmediately only)")
	fmt.Println("  /gettask [id]       get task snapshot")
	fmt.Println("  /subscribe [id]     subscribe task event stream")
	fmt.Println("  /cancel [id]        cancel task")
	fmt.Println("  /new                clear contextID")
	fmt.Println("  /help               show this help")
	fmt.Println("  /quit               exit")
	fmt.Println()
}

// splitCommand parses "/cmd arg..." ; returns ok=false for plain text.
func splitCommand(input string) (cmd, arg string, ok bool) {
	if !strings.HasPrefix(input, "/") {
		return "", "", false
	}
	parts := strings.SplitN(input, " ", 2)
	cmd = strings.ToLower(parts[0])
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg, true
}

func resolveTaskID(arg, lastTaskID string) (string, error) {
	if arg != "" {
		return arg, nil
	}
	if lastTaskID != "" {
		return lastTaskID, nil
	}
	return "", fmt.Errorf("no task id; usage needs an id or a prior /async-long-task")
}

// chatSend sends one turn via message/send and prints the final reply.
func chatSend(c *client.A2AClient, msg protocol.Message) (taskID string, contextID *string, err error) {
	resp, err := c.SendMessage(context.Background(), protocol.SendMessageParams{Message: msg})
	if err != nil {
		return "", nil, err
	}

	switch {
	case resp.GetTask() != nil:
		task := resp.GetTask()
		printTaskSnapshot(task)
		if task.ContextID != "" {
			return task.ID, &task.ContextID, nil
		}
		return task.ID, nil, nil
	case resp.GetMessage() != nil:
		reply := resp.GetMessage()
		fmt.Printf("message: %s\n", joinText(reply.Parts))
		return "", reply.ContextID, nil
	default:
		fmt.Println("(empty response)")
	}
	return "", nil, nil
}

// chatAsync sends with returnImmediately and prints the immediate task snapshot.
func chatAsync(c *client.A2AClient, text string, contextID *string) (taskID string, nextContextID *string, err error) {
	returnImmediately := true
	msg := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(text)},
		nil,
		contextID,
	)
	resp, err := c.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: msg,
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	if err != nil {
		return "", nil, err
	}
	task := resp.GetTask()
	if task == nil {
		if reply := resp.GetMessage(); reply != nil {
			fmt.Printf("message: %s\n", joinText(reply.Parts))
			return "", reply.ContextID, nil
		}
		return "", nil, fmt.Errorf("expected an immediate task snapshot")
	}
	fmt.Printf("async task: id=%s state=%s\n", task.ID, task.Status.State)
	printTaskSnapshot(task)
	if task.ContextID != "" {
		return task.ID, &task.ContextID, nil
	}
	return task.ID, nil, nil
}

// chatStream sends one turn via message/stream and prints events as they arrive.
func chatStream(c *client.A2AClient, msg protocol.Message) (taskID string, contextID *string, err error) {
	events, err := c.StreamMessage(context.Background(), protocol.SendMessageParams{Message: msg})
	if err != nil {
		return "", nil, err
	}
	return drainEvents(events)
}

func getTask(c *client.A2AClient, taskID string) error {
	task, err := c.GetTasks(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		return err
	}
	fmt.Printf("task snapshot: id=%s state=%s artifacts=%d\n",
		task.ID, task.Status.State, len(task.Artifacts))
	printTaskSnapshot(task)
	return nil
}

func subscribeTask(c *client.A2AClient, taskID string) error {
	// Successful SubscribeToTask already emits the current Task snapshot as the
	// first SSE event, then live deltas. Terminal tasks are rejected by the
	// server — fall back to GetTasks so the user still sees the final state.
	events, err := c.ResubscribeTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		fmt.Printf("Subscribe unavailable: %v\n", err)
		fmt.Println("Falling back to GetTask...")
		return getTask(c, taskID)
	}
	fmt.Printf("subscribed %s\n", taskID)
	_, _, err = drainEvents(events)
	if err != nil {
		return err
	}
	return nil
}

func cancelTask(c *client.A2AClient, taskID string) error {
	task, err := c.CancelTasks(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		return err
	}
	fmt.Printf("cancel result: id=%s state=%s\n", task.ID, task.Status.State)
	printTaskSnapshot(task)
	return nil
}

// streamPrinter formats SubscribeToTask / message/stream events for the REPL.
type streamPrinter struct {
	taskID        string
	contextID     *string
	liveOpen      bool
	artifactNames map[string]string
}

func drainEvents(events <-chan protocol.StreamResponse) (taskID string, contextID *string, err error) {
	p := &streamPrinter{artifactNames: map[string]string{}}
	for event := range events {
		p.handle(event)
	}
	p.endLive()
	return p.taskID, p.contextID, nil
}

func (p *streamPrinter) handle(event protocol.StreamResponse) {
	switch {
	case event.GetStatusUpdate() != nil:
		p.onStatus(event.GetStatusUpdate())
	case event.GetArtifactUpdate() != nil:
		p.onArtifact(event.GetArtifactUpdate())
	case event.GetMessage() != nil:
		p.onMessage(event.GetMessage())
	case event.GetTask() != nil:
		p.onTask(event.GetTask())
	}
}

func (p *streamPrinter) endLive() {
	if p.liveOpen {
		fmt.Println()
		p.liveOpen = false
	}
}

func (p *streamPrinter) noteIDs(taskID, contextID string) {
	if taskID != "" {
		p.taskID = taskID
	}
	if contextID != "" {
		cid := contextID
		p.contextID = &cid
	}
}

func (p *streamPrinter) onStatus(su *protocol.TaskStatusUpdateEvent) {
	p.endLive()
	p.noteIDs(su.TaskID, su.ContextID)
	fmt.Printf("  status: %s\n", su.Status.State)
	if su.Status.Message != nil && su.Final {
		fmt.Printf("  status message: %s\n", joinText(su.Status.Message.Parts))
	}
}

func (p *streamPrinter) onArtifact(au *protocol.TaskArtifactUpdateEvent) {
	p.noteIDs(au.TaskID, au.ContextID)
	name := rememberArtifactName(p.artifactNames, au.Artifact)
	text := joinText(au.Artifact.Parts)
	if text == "" {
		return
	}
	if !p.liveOpen {
		fmt.Printf("  artifact[%s]> ", name)
		p.liveOpen = true
	}
	fmt.Print(text)
	_ = os.Stdout.Sync() // show each word promptly (stdout is line-buffered)
	if au.LastChunk != nil && *au.LastChunk {
		fmt.Println()
		p.liveOpen = false
	}
}

func (p *streamPrinter) onMessage(reply *protocol.Message) {
	p.endLive()
	fmt.Printf("  message: %s\n", joinText(reply.Parts))
	if reply.ContextID != nil {
		p.contextID = reply.ContextID
	}
}

func (p *streamPrinter) onTask(task *protocol.Task) {
	p.endLive()
	p.taskID = task.ID
	p.noteIDs(task.ID, task.ContextID)
	// First SubscribeToTask frame: metadata + seed the live line with
	// whatever text is already aggregated, then continue with deltas.
	fmt.Printf("  snapshot: id=%s state=%s\n", task.ID, task.Status.State)
	if task.Status.Message != nil {
		if text := joinText(task.Status.Message.Parts); text != "" {
			fmt.Printf("  status message: %s\n", text)
		}
	}
	for _, a := range task.Artifacts {
		name := rememberArtifactName(p.artifactNames, a)
		text := joinText(a.Parts)
		if text == "" {
			continue
		}
		fmt.Printf("  artifact[%s]> %s", name, text)
		_ = os.Stdout.Sync()
		p.liveOpen = true
	}
}

func rememberArtifactName(names map[string]string, a protocol.Artifact) string {
	if a.Name != nil && *a.Name != "" {
		names[a.ArtifactID] = *a.Name
	}
	if name := names[a.ArtifactID]; name != "" {
		return name
	}
	if a.ArtifactID != "" {
		return a.ArtifactID
	}
	return "artifact"
}

func printTaskSnapshot(task *protocol.Task) {
	// Status.message and Artifacts are different fields — label them explicitly
	// so a short completion note is not mistaken for the final artifact text.
	if task.Status.Message != nil {
		if text := joinText(task.Status.Message.Parts); text != "" {
			fmt.Printf("  status message: %s\n", text)
		}
	}
	for _, artifact := range task.Artifacts {
		text := joinText(artifact.Parts)
		if text == "" {
			continue
		}
		fmt.Printf("  final artifact[%s]: %s\n", deref(artifact.Name), text)
	}
}

func joinText(parts []*protocol.Part) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.TextContent())
	}
	return b.String()
}

func deref(s *string) string {
	if s == nil {
		return "artifact"
	}
	return *s
}
