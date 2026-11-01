package main

// `jevlin mcp`: the machine paths, served over the Model Context Protocol.
//
// A host that prefers MCP to a shell skill starts this command and holds it
// open; it lives exactly as long as the host keeps its stdin. The transport
// is MCP's stdio one: newline-delimited JSON-RPC 2.0, one message per line,
// on stdin and stdout. stdout carries nothing else, ever, because a host
// reads every byte of it as protocol; diagnostics go to stderr, and only
// with -log.
//
// The tools are the machine paths themselves, not a second implementation
// of them. tools/call hands its arguments to searchMain or fetchMain as the
// --stdin request, so the validation, the unknown-field refusal, the error
// codes, the setup answers, the trace and the envelope are the same code a
// skill's call runs. A refused or failed call is a tool result with isError
// set and the envelope as its text, never a JSON-RPC error: the agent reads
// the same ok/code/action it would read from the command.
//
// Requests are served one at a time, in the order they arrive. A search is
// the only slow thing here, a host waits on each tool call anyway, and one
// reader with one writer needs no locking on stdout. It also settles the
// cancellation notification: by the time one is read, the request it names
// has already been answered, so it is accepted and ignored.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// mcpProtocolVersions are the protocol revisions this server speaks, newest
// first. A tools-only server answers the same in each; the differences that
// matter here are structuredContent, sent from 2025-06-18 on.
var mcpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const mcpStructuredContentSince = "2025-06-18"

// mcpCancelled is the protocol's cancellation notification, spelled as the
// specification spells it.
const mcpCancelled = "notifications/cancelled" //nolint:misspell // the method name is the protocol's, not ours to respell

// JSON-RPC 2.0's error codes, the ones this server answers with.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// mcpHarnessName is the part of an MCP client's own name that may ride as
// the trace's harness: a token, so the client's say-so cannot put anything
// but a name there, and short enough that "mcp:" and it fit the router's
// 64-character bound on the field.
var mcpHarnessName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$`)

// mcpMethods are the requests this server answers, and the only method
// names its log repeats: any other is logged as unknown, so nothing a
// client wrote reaches the log as it was sent.
var mcpMethods = []string{"initialize", "ping", "tools/list", "tools/call"}

type mcpServer struct {
	out    io.Writer
	logw   io.Writer
	getenv func(string) string
	// cfgArgs is the -config this command was given, handed on to every
	// tool call so a tool reads the same installation the server was
	// started for.
	cfgArgs  []string
	search   searchOps
	now      func() time.Time
	protocol string
	// harness is the trace's harness for this client's searches: "mcp",
	// or "mcp:" and the name the client gave in initialize. It never
	// equals a name a hook writes, so a search here is never threaded
	// into a lineage file some other session of the same host left in
	// the working directory; each host process is one session, keyed by
	// its pid as for any search with no hook channel.
	harness string
}

func cmdMCP(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	ops := realSearchOps()
	return mcpMain(ops, time.Now, args, stdin, stdout, stderr, getenv)
}

func mcpMain(ops searchOps, now func() time.Time, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := newFlagSet("mcp", stderr)
	cfgPath := onceStringFlag(fs, "config", "path to TOML config file; given at most once")
	logOn := fs.Bool("log", false, "write one line per message to stderr: the method, the tool and the outcome, never a request's content")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(fs.Args()) > 0 {
		fmt.Fprintln(stderr, "jevlin mcp: takes no arguments; a host starts it and speaks MCP on stdin and stdout")
		return exitUsage
	}
	s := &mcpServer{out: stdout, logw: io.Discard, getenv: getenv, search: ops, now: now, harness: "mcp"}
	if *logOn {
		s.logw = stderr
	}
	if *cfgPath != "" {
		s.cfgArgs = []string{"-config", *cfgPath}
	}
	s.search.hook.getenv = s.env
	// No signal handler for the calls: a host that stops the server with a
	// signal mid-search stops the server, not only that search.
	s.search.cancelOn = plainContext
	return s.serve(stdin)
}

// serve reads messages until stdin ends. End of input is the host closing
// the server, and the normal way out.
func (s *mcpServer) serve(stdin io.Reader) int {
	br := bufio.NewReaderSize(stdin, 64<<10)
	for {
		line, tooLarge, err := readBoundedLine(br, machineStdinMax)
		switch {
		case tooLarge:
			s.logf("message over the 1 MiB limit refused")
			s.reply(rpcErrorResponse(nil, rpcInvalidRequest, "the message is larger than the 1 MiB limit"))
		case len(bytes.TrimSpace(line)) > 0:
			s.handle(line)
		}
		if errors.Is(err, io.EOF) {
			return exitOK
		}
		if err != nil {
			s.logf("reading stdin failed")
			return exitTransport
		}
	}
}

// readBoundedLine reads one line without holding more than max bytes of it.
// A longer line is read to its end and dropped, so the next line is read
// from its own start.
func readBoundedLine(br *bufio.Reader, max int) (line []byte, tooLarge bool, err error) {
	for {
		chunk, rerr := br.ReadSlice('\n')
		if !tooLarge {
			if len(line)+len(chunk) > max+1 { // +1: the newline itself
				tooLarge, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		if tooLarge {
			return nil, true, rerr
		}
		return bytes.TrimRight(line, "\r\n"), false, rerr
	}
}

func (s *mcpServer) env(k string) string {
	if k == "JEVLIN_HARNESS" {
		// A harness the host's environment declares is the host's word,
		// as it is for every search.
		if v := s.getenv(k); v != "" {
			return v
		}
		return s.harness
	}
	return s.getenv(k)
}

func (s *mcpServer) logf(format string, a ...any) {
	fmt.Fprintf(s.logw, "jevlin mcp: "+format+"\n", a...)
}

// rpcMessage is a message's members, each kept raw, so that a member of
// the wrong type is told apart from bytes that are not JSON at all: the
// first is an invalid request, answered with its id when it has a usable
// one; only the second is a parse error.
type rpcMessage struct {
	JSONRPC json.RawMessage `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  json.RawMessage `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func rpcErrorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	if id == nil {
		id = json.RawMessage("null")
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// reply writes one message and one newline. Nothing else in this command
// writes to stdout: the tools write their envelopes into a buffer.
func (s *mcpServer) reply(r rpcResponse) {
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(rpcErrorResponse(r.ID, rpcInternalError, "the answer could not be encoded"))
	}
	_, _ = s.out.Write(append(b, '\n'))
}

// validRequestID is JSON-RPC's id as MCP narrows it: a string or a number,
// never null.
func validRequestID(id json.RawMessage) bool {
	t := bytes.TrimSpace(id)
	if len(t) == 0 {
		return false
	}
	switch t[0] {
	case '"':
		var s string
		return json.Unmarshal(t, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		return json.Unmarshal(t, &n) == nil
	}
	return false
}

func (s *mcpServer) handle(line []byte) {
	if !json.Valid(line) {
		s.logf("unparsable message refused")
		s.reply(rpcErrorResponse(nil, rpcParseError, "the message is not valid JSON"))
		return
	}
	if bytes.TrimSpace(line)[0] == '[' {
		s.reply(rpcErrorResponse(nil, rpcInvalidRequest, "batches are not supported"))
		return
	}
	var m rpcMessage
	if err := json.Unmarshal(line, &m); err != nil {
		s.reply(rpcErrorResponse(nil, rpcInvalidRequest, "a message must be a JSON object"))
		return
	}
	var id json.RawMessage
	if validRequestID(m.ID) {
		id = m.ID
	}
	if len(m.Method) == 0 {
		if len(m.Result) > 0 || len(m.Error) > 0 {
			return // an answer to a request this server never sends
		}
		s.reply(rpcErrorResponse(id, rpcInvalidRequest, "the message names no method"))
		return
	}
	var method string
	if isJSONNull(m.Method) || json.Unmarshal(m.Method, &method) != nil {
		s.reply(rpcErrorResponse(id, rpcInvalidRequest, `"method" must be a string`))
		return
	}
	if len(m.ID) == 0 {
		s.notification(method)
		return
	}
	var version string
	if json.Unmarshal(m.JSONRPC, &version) != nil || version != "2.0" || id == nil {
		s.reply(rpcErrorResponse(id, rpcInvalidRequest, `a request needs "jsonrpc": "2.0" and a string or number id`))
		return
	}
	s.reply(s.request(id, method, m.Params))
}

// notification handles the notifications a client sends. None is answered,
// an unknown one included, as JSON-RPC requires.
func (s *mcpServer) notification(method string) {
	switch method {
	case "notifications/initialized":
		s.logf("initialized")
	case mcpCancelled:
		// Requests are served in order, so the one this names has been
		// answered already.
		s.logf("cancellation noted; requests are answered in order, so there is nothing left to stop")
	}
}

func (s *mcpServer) request(id json.RawMessage, method string, params json.RawMessage) (resp rpcResponse) {
	logged := "an unknown method"
	if slices.Contains(mcpMethods, method) {
		logged = method
	}
	defer func() {
		if recover() != nil {
			s.logf("%s failed inside the server", logged)
			resp = rpcErrorResponse(id, rpcInternalError, "the server failed while answering")
		}
	}()
	result, rerr := s.dispatch(method, params)
	if rerr != nil {
		s.logf("%s: error %d", logged, rerr.Code)
		return rpcResponse{JSONRPC: "2.0", ID: id, Error: rerr}
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *mcpServer) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return s.initialize(params)
	case "ping":
		s.logf("ping")
		return struct{}{}, nil
	case "tools/list":
		s.logf("tools/list")
		return map[string]any{"tools": mcpTools()}, nil
	case "tools/call":
		return s.callTool(params)
	}
	return nil, &rpcError{Code: rpcMethodNotFound, Message: "this server has no method " + strconv.Quote(method)}
}

// paramsObject decodes params as the JSON object each method takes. Absent
// params are an empty object.
func paramsObject(params json.RawMessage, v any) bool {
	t := bytes.TrimSpace(params)
	if len(t) == 0 {
		return true
	}
	if t[0] != '{' {
		return false
	}
	return json.Unmarshal(t, v) == nil
}

func (s *mcpServer) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion *string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if !paramsObject(params, &p) || p.ProtocolVersion == nil {
		return nil, &rpcError{Code: rpcInvalidParams, Message: `initialize needs "protocolVersion"`}
	}
	// A version this server speaks is answered with itself; any other with
	// the newest this server speaks, which the client may then refuse.
	s.protocol = mcpProtocolVersions[0]
	if slices.Contains(mcpProtocolVersions, *p.ProtocolVersion) {
		s.protocol = *p.ProtocolVersion
	}
	s.harness = "mcp"
	if mcpHarnessName.MatchString(p.ClientInfo.Name) {
		s.harness = "mcp:" + p.ClientInfo.Name
	}
	s.logf("initialize: protocol %s", s.protocol)
	return map[string]any{
		"protocolVersion": s.protocol,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "jevlin", "version": buildVersion()},
	}, nil
}

type mcpToolResult struct {
	Content           []mcpText       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

type mcpText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *mcpServer) callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      *string         `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if !paramsObject(params, &p) || p.Name == nil {
		return nil, &rpcError{Code: rpcInvalidParams, Message: `tools/call needs "name"`}
	}
	var run func(stdin io.Reader, stdout io.Writer) int
	switch *p.Name {
	case "search":
		run = func(stdin io.Reader, stdout io.Writer) int {
			return searchMain(s.search, append([]string{"--stdin"}, s.cfgArgs...), stdin, stdout, io.Discard, s.env)
		}
	case "fetch":
		run = func(stdin io.Reader, stdout io.Writer) int {
			return fetchMainIn(s.search.cancelOn, s.now, append([]string{"--stdin"}, s.cfgArgs...), stdin, stdout, io.Discard, s.env)
		}
	default:
		return nil, &rpcError{Code: rpcInvalidParams, Message: "this server has no tool " + strconv.Quote(*p.Name)}
	}
	body, ok := machineRequestOf(p.Arguments)
	if !ok {
		return nil, &rpcError{Code: rpcInvalidParams, Message: `"arguments" must be an object`}
	}
	var envelope bytes.Buffer
	run(bytes.NewReader(body), &envelope)
	var header struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	text := strings.TrimRight(envelope.String(), "\n")
	if json.Unmarshal(envelope.Bytes(), &header) != nil {
		s.logf("tools/call %s: no envelope", *p.Name)
		return mcpToolResult{Content: []mcpText{{Type: "text", Text: "the " + *p.Name + " produced no answer"}}, IsError: true}, nil
	}
	s.logf("tools/call %s: %s", *p.Name, orString(header.Code, "ok"))
	r := mcpToolResult{Content: []mcpText{{Type: "text", Text: text}}, IsError: !header.OK}
	if s.protocol >= mcpStructuredContentSince {
		r.StructuredContent = json.RawMessage(text)
	}
	return r, nil
}

// machineRequestOf is a tool's arguments as the version-1 request its
// machine path reads: the same object, with "version": 1 added when the
// caller left it out, so an MCP client need not repeat what the tool
// already fixes. Every value is passed on as the caller's bytes; a
// "version" the caller did write is left for the machine path to judge,
// in any case of its letters, because encoding/json matches the machine
// path's keys without regard to case: {"Version": 2} is a version 2
// request there, and must not become a version 1 one here.
func machineRequestOf(args json.RawMessage) ([]byte, bool) {
	t := bytes.TrimSpace(args)
	if len(t) == 0 {
		t = []byte("{}")
	}
	if t[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(t, &fields) != nil {
		return nil, false
	}
	named := false
	for k := range fields {
		named = named || strings.EqualFold(k, "version")
	}
	if !named {
		fields["version"] = json.RawMessage("1")
	}
	b, err := json.Marshal(fields)
	return b, err == nil
}
