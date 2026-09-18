package notesd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gig3m/applenotes/internal/notesapp"
	"github.com/gig3m/applenotes/internal/notestore"
)

// MCP over HTTP, so an agent can use a note library the way it uses any other
// tool. JSON-RPC 2.0 on POST /mcp, behind the same bearer token as the REST
// routes -- an MCP endpoint that forgot auth would be the whole library,
// unauthenticated.

const mcpProtocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&req); err != nil || req.Method == "" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "applenotes", "version": "1"},
		}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools}
	case "tools/call":
		resp.Result = s.callTool(r.Context(), req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	writeJSON(w, http.StatusOK, resp)
}

// toolResult is MCP's content envelope. A tool that fails reports it here, in
// band: a model needs to read "that note does not exist" as an answer, not lose
// the connection.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func toolJSON(v any) map[string]any {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolResult("could not encode the result: "+err.Error(), true)
	}
	return toolResult(string(b), false)
}

var mcpTools = []map[string]any{
	{
		"name":        "list_notes",
		"description": "List notes, newest first. Returns uuid, title, folder and timestamps, not bodies.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"folder":  map[string]any{"type": "string", "description": "Limit to a folder, by name or uuid."},
				"deleted": map[string]any{"type": "boolean", "description": "Include notes in Recently Deleted."},
			},
		},
	},
	{
		"name":        "get_note",
		"description": "Read one note as Markdown, by uuid. Also reports what a rewrite would flatten or destroy.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"uuid": map[string]any{"type": "string"}},
			"required":   []string{"uuid"},
		},
	},
	{
		"name":        "list_folders",
		"description": "List note folders.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "create_note",
		"description": "Create a note from Markdown. The first line becomes the title. " +
			"Notes.app persists on its own schedule, so the new note may not be readable immediately.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"markdown": map[string]any{"type": "string"},
				"folder":   map[string]any{"type": "string", "description": "Folder name; the default folder if omitted."},
			},
			"required": []string{"markdown"},
		},
	},
	{
		"name": "append_note",
		"description": "Append Markdown to a note. Refuses if the note holds attachments or checklists, " +
			"which rewriting would destroy.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uuid":     map[string]any{"type": "string"},
				"markdown": map[string]any{"type": "string"},
			},
			"required": []string{"uuid", "markdown"},
		},
	},
	{
		"name": "replace_note",
		"description": "Replace a note's whole body with Markdown. Refuses if that would destroy " +
			"attachments or checklists unless force is set.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uuid":     map[string]any{"type": "string"},
				"markdown": map[string]any{"type": "string"},
				"force":    map[string]any{"type": "boolean", "description": "Overwrite even if content would be lost."},
			},
			"required": []string{"uuid", "markdown"},
		},
	},
	{
		"name":        "delete_note",
		"description": "Move a note to Recently Deleted, where Notes keeps it for 30 days.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"uuid": map[string]any{"type": "string"}},
			"required":   []string{"uuid"},
		},
	},
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) map[string]any {
	var call struct {
		Name      string `json:"name"`
		Arguments struct {
			UUID     string `json:"uuid"`
			Folder   string `json:"folder"`
			Markdown string `json:"markdown"`
			Deleted  bool   `json:"deleted"`
			Force    bool   `json:"force"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return toolResult("bad arguments: "+err.Error(), true)
	}
	a := call.Arguments

	switch call.Name {
	case "list_notes":
		notes, err := s.store.Notes(notestore.ListOptions{Folder: a.Folder, IncludeDeleted: a.Deleted})
		if err != nil {
			return toolResult(err.Error(), true)
		}
		out := make([]noteJSON, 0, len(notes))
		for _, n := range notes {
			out = append(out, metaJSON(n))
		}
		return toolJSON(out)

	case "list_folders":
		fs, err := s.store.Folders()
		if err != nil {
			return toolResult(err.Error(), true)
		}
		out := make([]folderJSON, 0, len(fs))
		for _, f := range fs {
			out = append(out, folderJSON{UUID: f.UUID, Name: f.Name, Trash: f.Trash()})
		}
		return toolJSON(out)

	case "get_note":
		meta, err := s.store.Meta(a.UUID)
		if err != nil {
			return toolResult(err.Error(), true)
		}
		out := metaJSON(meta)
		if body, err := s.store.Body(a.UUID); err == nil {
			out.Markdown = body.Markdown()
			out.Degrades = body.Degrades()
			out.Destroys = body.Destroys()
		} else {
			out.BodyError = err.Error()
		}
		return toolJSON(out)

	case "create_note":
		if strings.TrimSpace(a.Markdown) == "" {
			return toolResult("markdown must not be empty", true)
		}
		uuid, err := notesapp.New(s.store).Create(ctx, a.Folder, a.Markdown)
		if err != nil && uuid == "" {
			return toolResult(writeAdvice(err), true)
		}
		return toolJSON(map[string]any{"uuid": uuid, "accepted": true,
			"note": "Notes.app persists on its own schedule; this may not be readable yet."})

	case "append_note":
		if strings.TrimSpace(a.Markdown) == "" {
			return toolResult("markdown must not be empty", true)
		}
		degraded, err := notesapp.New(s.store).Append(ctx, a.UUID, a.Markdown)
		if err != nil {
			return toolResult(writeAdvice(err), true)
		}
		return toolJSON(map[string]any{"accepted": true, "degraded": degraded})

	case "replace_note":
		if strings.TrimSpace(a.Markdown) == "" {
			return toolResult("markdown must not be empty", true)
		}
		var degraded []string
		var err error
		if a.Force {
			err = notesapp.New(s.store).ReplaceForce(ctx, a.UUID, a.Markdown)
		} else {
			degraded, err = notesapp.New(s.store).Replace(ctx, a.UUID, a.Markdown)
		}
		if err != nil {
			return toolResult(writeAdvice(err), true)
		}
		return toolJSON(map[string]any{"accepted": true, "degraded": degraded})

	case "delete_note":
		if err := notesapp.New(s.store).Delete(ctx, a.UUID); err != nil {
			return toolResult(writeAdvice(err), true)
		}
		return toolJSON(map[string]any{"accepted": true})
	}
	return toolResult("unknown tool: "+call.Name, true)
}

// writeAdvice turns a write failure into something a model can act on rather
// than retry blindly.
func writeAdvice(err error) string {
	var lossy *notesapp.ErrLossyRewrite
	if errors.As(err, &lossy) {
		return fmt.Sprintf("%s. Retry with force set to true only if losing %s is acceptable.",
			err.Error(), strings.Join(lossy.Features, " and "))
	}
	return err.Error()
}
