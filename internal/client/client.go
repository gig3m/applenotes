// Package client talks to notesd from a machine that is not the Mac.
//
// Every failure mode here is about a server the caller does not control: the
// Mac can be asleep, rebooting, or answering with a proxy's error page. The
// governing rule is that none of those may look like an empty result, because a
// caller that then rewrites a note would be rewriting it from nothing.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxResponse bounds how much a server can make this client allocate.
const MaxResponse = 64 << 20

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// Generous, because an Apple Event behind this can take a while, but
		// finite: a TUI must not hang on a sleeping Mac.
		HTTP: &http.Client{Timeout: 90 * time.Second},
	}
}

// Note mirrors notesd's JSON. Trashed, not Deleted: a note reaches the trash by
// more than one route and the flag alone misses some of them.
type Note struct {
	UUID     string    `json:"uuid"`
	Title    string    `json:"title"`
	Folder   string    `json:"folder"`
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
	Trashed  bool      `json:"trashed"`
	Pinned   bool      `json:"pinned"`
	Locked   bool      `json:"locked"`
	DeepLink string    `json:"deepLink"`
	Markdown string    `json:"markdown,omitempty"`
	Degrades []string  `json:"degrades,omitempty"`
	Destroys []string  `json:"destroys,omitempty"`
}

type Folder struct {
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	Trash bool   `json:"trash"`
}

type ListOptions struct {
	Folder         string
	IncludeDeleted bool
}

// RefusedError reports a rewrite the server declined because it would destroy
// content. It is typed so a caller can offer force instead of guessing from
// prose.
type RefusedError struct {
	Message  string
	Destroys []string
}

func (e *RefusedError) Error() string { return e.Message }

// StatusError is any other unsuccessful response.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("notesd: %s", http.StatusText(e.Code))
	}
	return fmt.Sprintf("notesd: %s (%d)", e.Message, e.Code)
}

func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	var out []Folder
	return out, c.do(ctx, http.MethodGet, "/v1/folders", nil, &out)
}

func (c *Client) Notes(ctx context.Context, opt ListOptions) ([]Note, error) {
	q := url.Values{}
	if opt.Folder != "" {
		q.Set("folder", opt.Folder)
	}
	if opt.IncludeDeleted {
		q.Set("deleted", "true")
	}
	path := "/v1/notes"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []Note
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

// Hit is one search result: a note plus the matching text in context.
type Hit struct {
	Note
	Context string `json:"context"`
	Score   int    `json:"score"`
}

func (c *Client) Search(ctx context.Context, query, folder string, deleted bool) ([]Hit, error) {
	q := url.Values{"q": {query}}
	if folder != "" {
		q.Set("folder", folder)
	}
	if deleted {
		q.Set("deleted", "true")
	}
	var out []Hit
	return out, c.do(ctx, http.MethodGet, "/v1/search?"+q.Encode(), nil, &out)
}

func (c *Client) Note(ctx context.Context, uuid string) (Note, error) {
	var out Note
	return out, c.do(ctx, http.MethodGet, "/v1/notes/"+url.PathEscape(uuid), nil, &out)
}

type writeRequest struct {
	Markdown string `json:"markdown"`
	Folder   string `json:"folder,omitempty"`
	Force    bool   `json:"force,omitempty"`
}

type writeResponse struct {
	UUID     string   `json:"uuid"`
	Degraded []string `json:"degraded"`
	Detail   string   `json:"detail"`
}

// Create makes a note and returns its UUID, which may be empty: the Mac's Notes
// app persists on its own schedule, so a freshly created note is not always in
// the database yet.
func (c *Client) Create(ctx context.Context, folder, markdown string) (string, error) {
	var out writeResponse
	err := c.do(ctx, http.MethodPost, "/v1/notes", writeRequest{Markdown: markdown, Folder: folder}, &out)
	return out.UUID, err
}

// Replace returns the formatting the rewrite flattened.
func (c *Client) Replace(ctx context.Context, uuid, markdown string, force bool) ([]string, error) {
	var out writeResponse
	err := c.do(ctx, http.MethodPut, "/v1/notes/"+url.PathEscape(uuid),
		writeRequest{Markdown: markdown, Force: force}, &out)
	return out.Degraded, err
}

func (c *Client) Append(ctx context.Context, uuid, markdown string) ([]string, error) {
	var out writeResponse
	err := c.do(ctx, http.MethodPost, "/v1/notes/"+url.PathEscape(uuid)+"/append",
		writeRequest{Markdown: markdown}, &out)
	return out.Degraded, err
}

func (c *Client) Delete(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodDelete, "/v1/notes/"+url.PathEscape(uuid), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	// In the header, never the query string: a URL ends up in logs and shell
	// history.
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("notesd at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return fmt.Errorf("reading the response: %w", err)
	}
	if len(raw) > MaxResponse {
		return fmt.Errorf("notesd returned more than %d bytes", MaxResponse)
	}

	if resp.StatusCode >= 400 {
		return statusError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	// Decoded strictly: a proxy's HTML error page with a 200 must not silently
	// decode to an empty list.
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("notesd returned something that is not the expected JSON: %w", err)
	}
	return nil
}

func statusError(code int, raw []byte) error {
	var body struct {
		Error    string   `json:"error"`
		Destroys []string `json:"destroys"`
	}
	_ = json.Unmarshal(raw, &body)
	if code == http.StatusConflict {
		msg := body.Error
		if msg == "" {
			msg = "the server refused to rewrite this note"
		}
		return &RefusedError{Message: msg, Destroys: body.Destroys}
	}
	return &StatusError{Code: code, Message: body.Error}
}
