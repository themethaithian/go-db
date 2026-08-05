// Package api is a thin HTTP adapter over the App Service facade: a
// localhost-only API that lets a second process — the MCP proxy — reach the
// same facade the Wails shell binds directly, without duplicating the
// Approval Gate or the Connection Registry. See docs/adr/0001-process-model.md
// for why the bind is loopback-only and guarded by a per-launch token file
// rather than anything broader.
//
// This package owns no domain logic. It classifies nothing, previews
// nothing, decides nothing — every endpoint calls straight through to
// *service.AppService and marshals what comes back. It is a pipe, not a
// policy.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

const (
	// tokenFile is the name of the file Start writes inside the Server's
	// directory. Its shape is {"port": <n>, "token": "<hex>"}.
	tokenFile = "api.json"

	maxBodyBytes = 1 << 20 // 1 MB: generous for a SQL statement, small enough to bound abuse.

	readTimeout = 5 * time.Second
	idleTimeout = 60 * time.Second

	// writeTimeout has to outlast the longest legitimate response, and the
	// longest one this API has is a mutating query: it blocks in the Approval
	// Console until a human answers it or its deadline passes. A deadline
	// shorter than that would cut the connection at the worst possible moment —
	// the human approves, the mutation runs, and the agent that asked for it is
	// told the app went away. The margin covers the query's own execution
	// afterwards.
	//
	// It is set on the whole server rather than per route because every other
	// endpoint answers in microseconds, and a generous deadline on a
	// loopback-only, token-gated listener buys an attacker who already has the
	// token nothing they did not already have.
	writeTimeout = guard.ApprovalTimeout + 60*time.Second

	shutdownTimeout = 5 * time.Second
)

// Server is the localhost HTTP adapter over the App Service facade.
//
// Start binds 127.0.0.1 on a per-launch random port, generates a random
// bearer token, and writes both to an owner-only file so a co-operating
// process — never a browser, never another user — can find and use them.
// Close shuts the server down and removes the file.
//
// A Server is used once: construct it, Start it, Close it. It is safe for
// concurrent use once started.
type Server struct {
	svc *service.AppService
	dir string

	mu       sync.Mutex
	listener net.Listener
	http     *http.Server
	token    string

	wg sync.WaitGroup
}

// New returns a Server backed by svc, whose token file is written inside dir.
// The app passes its config directory; tests pass t.TempDir().
func New(svc *service.AppService, dir string) *Server {
	return &Server{svc: svc, dir: dir}
}

// tokenFilePayload is the on-disk shape of the token file.
type tokenFilePayload struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// Start binds a loopback listener on a random port, generates a per-launch
// token, and writes the token file — in that order, so nothing that reads the
// file ever finds a port that is not yet listening. It returns once the
// server is accepting connections.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		return errors.New("api: already started")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("api: listening: %w", err)
	}

	token, err := randomToken()
	if err != nil {
		ln.Close()
		return fmt.Errorf("api: generating token: %w", err)
	}
	s.token = token

	mux := http.NewServeMux()
	s.routes(mux)

	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	s.listener = ln
	s.http = httpServer

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "go-db: local API server:", err)
		}
	}()

	if err := s.writeTokenFile(tcpPort(ln)); err != nil {
		httpServer.Close()
		s.wg.Wait()
		s.listener = nil
		s.http = nil
		return err
	}

	return nil
}

// Close shuts the server down, waits for it to stop accepting connections,
// and removes the token file. Close on a Server that was never started, or
// was already closed, is a no-op.
func (s *Server) Close() error {
	s.mu.Lock()
	httpServer := s.http
	s.http = nil
	s.listener = nil
	s.mu.Unlock()

	if httpServer == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := httpServer.Shutdown(ctx)
	s.wg.Wait()

	if err := os.Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		if shutdownErr == nil {
			shutdownErr = fmt.Errorf("api: removing token file: %w", err)
		}
	}
	return shutdownErr
}

// Addr returns the bound loopback address ("127.0.0.1:PORT"), or "" if the
// server is not currently running.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) path() string {
	return filepath.Join(s.dir, tokenFile)
}

// writeTokenFile writes the token file atomically — write a temp file in the
// same directory, then rename — the same pattern db.ProfileStore uses, so a
// reader never observes a half-written file.
func (s *Server) writeTokenFile(port int) error {
	data, err := json.Marshal(tokenFilePayload{Port: port, Token: s.token})
	if err != nil {
		return fmt.Errorf("api: encoding token file: %w", err)
	}

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("api: creating config dir: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, ".api-*.json")
	if err != nil {
		return fmt.Errorf("api: creating token file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("api: writing token file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("api: securing token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("api: writing token file: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return fmt.Errorf("api: replacing token file: %w", err)
	}
	return nil
}

// routes registers every endpoint, each behind the auth middleware. There is
// no path that skips it: even /health proves the token pipe works.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.withAuth(s.handleHealth))
	mux.HandleFunc("/profiles", s.withAuth(s.handleProfiles))
	mux.HandleFunc("/connect", s.withAuth(s.handleConnect))
	mux.HandleFunc("/query", s.withAuth(s.handleQuery))
}

// withAuth enforces the two things every request must satisfy before a
// handler ever sees it: no Origin header (a browser always sends one; curl
// and the MCP proxy never do, so this alone defeats DNS-rebinding without a
// CORS allowlist to maintain) and a valid bearer token, compared in constant
// time so a wrong guess cannot be timed into a right one.
func (s *Server) withAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "cross-origin requests are not accepted")
			return
		}

		const prefix = "Bearer "
		got, hasPrefix := strings.CutPrefix(r.Header.Get("Authorization"), prefix)
		if !hasPrefix || !s.validToken(got) {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		handler(w, r)
	}
}

func (s *Server) validToken(candidate string) bool {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()

	return subtle.ConstantTimeCompare([]byte(token), []byte(candidate)) == 1
}

// handleHealth is the liveness probe the token pipe itself is proven by: a
// 200 here means the requester had the right token for the right port.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// profileInfo is one row of GET /profiles: a saved Profile's name and
// whether the Connection Registry currently holds it open.
type profileInfo struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	profiles, err := s.svc.ListProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	connected := make(map[string]bool, len(profiles))
	for _, name := range s.svc.ConnectedProfiles() {
		connected[name] = true
	}

	out := make([]profileInfo, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, profileInfo{Name: p.Name, Connected: connected[p.Name]})
	}
	writeJSON(w, http.StatusOK, out)
}

type connectRequest struct {
	Profile string `json:"profile"`
}

type connectResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Profile == "" {
		writeError(w, http.StatusBadRequest, "profile is required")
		return
	}

	if err := s.svc.Connect(r.Context(), req.Profile); err != nil {
		writeJSON(w, http.StatusOK, connectResponse{OK: false, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, connectResponse{OK: true})
}

// queryRequest is the body of POST /query. Origin defaults to guard.OriginAI
// when omitted: the only expected remote caller of this API is the MCP
// proxy, so a request that does not say who it is on behalf of is assumed to
// be one.
type queryRequest struct {
	Profile string       `json:"profile"`
	SQL     string       `json:"sql"`
	Origin  guard.Origin `json:"origin"`
}

// handleQuery passes one query to the facade and marshals what comes back.
//
// It can block for minutes and that is correct: an AI-originated mutation waits
// in the Approval Console inside this call, so the agent that asked for it gets
// the real outcome on the request it already made rather than a token to poll
// with. Two things make that safe, and both are load-bearing. The server's
// writeTimeout outlasts the approval deadline, and the request's own context is
// what the facade waits on — so an MCP client that dies takes its pending
// mutation out of the console with it instead of leaving a human to approve a
// statement nobody is listening for.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Profile == "" {
		writeError(w, http.StatusBadRequest, "profile is required")
		return
	}
	if req.Origin == "" {
		req.Origin = guard.OriginAI
	}
	if !req.Origin.Valid() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid origin %q: must be \"human\" or \"ai\"", req.Origin))
		return
	}

	result := s.svc.RunQuery(r.Context(), req.Profile, req.SQL, req.Origin)
	writeJSON(w, http.StatusOK, result)
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("api: reading random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func tcpPort(ln net.Listener) int {
	return ln.Addr().(*net.TCPAddr).Port
}
