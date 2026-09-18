// Package notesd serves Apple Notes over HTTP for machines that are not Macs.
//
// It reads from NoteStore.sqlite and writes through Notes.app, so every caveat
// of those paths applies here -- most importantly that Notes.app persists on its
// own schedule. A write is therefore acknowledged as accepted, never as done,
// and a read immediately after a write may not reflect it.
package notesd

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gig3m/applenotes/internal/applescript"
	"github.com/gig3m/applenotes/internal/notesapp"
	"github.com/gig3m/applenotes/internal/notestore"
)

// Server answers the HTTP API. Its store is read-only; writes go to Notes.app.
type Server struct {
	store *notestore.Store
	token string
}

func New(store *notestore.Store, token string) *Server {
	return &Server{store: store, token: token}
}

// Handler returns the routes. Every one requires the token, including healthz:
// on a tailnet there is no other perimeter, and an unauthenticated endpoint is
// an unauthenticated endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", s.healthz)
	mux.HandleFunc("GET /v1/folders", s.folders)
	mux.HandleFunc("GET /v1/notes", s.listNotes)
	mux.HandleFunc("GET /v1/notes/{uuid}", s.getNote)
	mux.HandleFunc("POST /v1/notes", s.createNote)
	mux.HandleFunc("PUT /v1/notes/{uuid}", s.replaceNote)
	mux.HandleFunc("POST /v1/notes/{uuid}/append", s.appendNote)
	mux.HandleFunc("DELETE /v1/notes/{uuid}", s.deleteNote)
	return s.authenticated(rejectUncleanPaths(jsonErrors(mux)))
}

// authenticated rejects anything without the exact bearer token. The comparison
// is constant-time, and every failure produces the same response: a client must
// not be able to tell a missing token from a wrong one.
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rejectUncleanPaths answers 404 rather than letting ServeMux redirect. A
// redirect on "/v1/notes/../../etc/passwd" tells a caller the path was
// interesting; a 404 tells them nothing.
func rejectUncleanPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p != path.Clean(p) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// jsonErrors makes ServeMux's own 404 and 405 responses JSON, so a client never
// has to parse prose to find out what happened.
func jsonErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if !rec.wrote {
			writeError(w, rec.status, http.StatusText(rec.status))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	// ServeMux writes a plain-text body for 404 and 405; suppress it and let
	// jsonErrors emit JSON instead.
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		return
	}
	r.wrote = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote && (r.status == http.StatusNotFound || r.status == http.StatusMethodNotAllowed) {
		return len(b), nil // swallowed; jsonErrors will answer
	}
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// --- handlers ---------------------------------------------------------------

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type folderJSON struct {
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	Trash bool   `json:"trash"`
}

func (s *Server) folders(w http.ResponseWriter, r *http.Request) {
	fs, err := s.store.Folders()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]folderJSON, 0, len(fs))
	for _, f := range fs {
		out = append(out, folderJSON{UUID: f.UUID, Name: f.Name, Trash: f.Trash()})
	}
	writeJSON(w, http.StatusOK, out)
}

type noteJSON struct {
	UUID     string    `json:"uuid"`
	Title    string    `json:"title"`
	Folder   string    `json:"folder"`
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
	// Trashed, not Deleted: a note reaches the trash by more than one route and
	// the flag alone misses some of them.
	Trashed  bool     `json:"trashed"`
	Pinned   bool     `json:"pinned"`
	Locked   bool     `json:"locked"`
	DeepLink string   `json:"deepLink"`
	Markdown string   `json:"markdown,omitempty"`
	Degrades []string `json:"degrades,omitempty"`
	Destroys []string `json:"destroys,omitempty"`
}

func metaJSON(m notestore.NoteMeta) noteJSON {
	return noteJSON{
		UUID: m.UUID, Title: m.Title, Folder: m.FolderName,
		Created: m.Created, Modified: m.Modified,
		Trashed: m.Trashed, Pinned: m.Pinned, Locked: m.Locked,
		DeepLink: m.DeepLink(),
	}
}

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.store.Notes(notestore.ListOptions{
		Folder:         r.URL.Query().Get("folder"),
		IncludeDeleted: r.URL.Query().Get("deleted") == "true",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]noteJSON, 0, len(notes))
	for _, n := range notes {
		out = append(out, metaJSON(n))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getNote(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	meta, err := s.store.Meta(uuid)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := metaJSON(meta)
	if body, err := s.store.Body(uuid); err == nil {
		out.Markdown = body.Markdown()
		// Reported so a client can warn before round-tripping the note.
		out.Degrades = body.Degrades()
		out.Destroys = body.Destroys()
	}
	writeJSON(w, http.StatusOK, out)
}

type writeRequest struct {
	Markdown string `json:"markdown"`
	Folder   string `json:"folder"`
	Force    bool   `json:"force"`
}

func (s *Server) createNote(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeWrite(w, r)
	if !ok {
		return
	}
	writer, degraded := s.writer()
	uuid, err := writer.Create(r.Context(), req.Folder, req.Markdown)
	if errors.Is(err, notesapp.ErrNotYetVisible) {
		// The note exists; Notes.app has not written it to the database yet, so
		// its portable id is not knowable. Saying so beats inventing one.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted": true,
			"detail":   "created, but not yet in the database, so its uuid is not known",
		})
		return
	}
	if err != nil {
		writeWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "uuid": uuid, "degraded": *degraded,
	})
}

func (s *Server) replaceNote(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeWrite(w, r)
	if !ok {
		return
	}
	writer, degraded := s.writer()
	var err error
	if req.Force {
		err = writer.ReplaceForce(r.Context(), r.PathValue("uuid"), req.Markdown)
	} else {
		err = writer.Replace(r.Context(), r.PathValue("uuid"), req.Markdown)
	}
	s.acknowledge(w, err, degraded)
}

func (s *Server) appendNote(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeWrite(w, r)
	if !ok {
		return
	}
	writer, degraded := s.writer()
	s.acknowledge(w, writer.Append(r.Context(), r.PathValue("uuid"), req.Markdown), degraded)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	writer, degraded := s.writer()
	// Deleting moves the note to Recently Deleted, where Notes keeps it for 30
	// days, so it is not guarded the way a rewrite is.
	s.acknowledge(w, writer.Delete(r.Context(), r.PathValue("uuid")), degraded)
}

// --- plumbing ---------------------------------------------------------------

// writer builds a Writer per request. OnDegrade is a field on the Writer, so a
// shared one would report one request's degradation to another.
func (s *Server) writer() (*notesapp.Writer, *[]string) {
	degraded := new([]string)
	w := notesapp.New(s.store)
	w.OnDegrade = func(features []string) { *degraded = features }
	return w, degraded
}

func (s *Server) acknowledge(w http.ResponseWriter, err error, degraded *[]string) {
	if err != nil {
		writeWriteError(w, err)
		return
	}
	// Accepted, not OK: Notes.app persists on its own schedule, so claiming the
	// change is durable would be a lie.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "degraded": *degraded,
	})
}

const maxBody = 4 << 20

func decodeWrite(w http.ResponseWriter, r *http.Request) (writeRequest, bool) {
	var req writeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a JSON object: "+err.Error())
		return req, false
	}
	if r.Method != http.MethodDelete && strings.TrimSpace(req.Markdown) == "" {
		writeError(w, http.StatusBadRequest, "markdown must not be empty")
		return req, false
	}
	return req, true
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, notestore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such note")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeWriteError(w http.ResponseWriter, err error) {
	var lossy *notesapp.ErrLossyRewrite
	if errors.As(err, &lossy) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    err.Error(),
			"destroys": lossy.Features,
			"detail":   "retry with force to overwrite anyway",
		})
		return
	}
	if errors.Is(err, notestore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such note")
		return
	}
	if errors.Is(err, applescript.ErrInvalidArgument) {
		// The content could not be passed to Notes at all -- too large, or
		// carrying bytes argv cannot hold. That is the request's fault.
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// Notes.app already has the event and may still apply it, so this is
		// not a statement that nothing happened.
		writeError(w, http.StatusGatewayTimeout, "timed out; the change may still have been applied")
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
