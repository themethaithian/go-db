package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/themethaithian/go-db/internal/guard"
)

// tokenFileName is the file internal/api.Server writes inside the app's
// config directory: {"port": <n>, "token": "<hex>"}. This package does not
// import internal/api — it is the MCP adapter and talks HTTP only — so it
// reads the file's JSON shape directly rather than the type that wrote it.
const tokenFileName = "api.json"

// requestTimeout bounds the calls that have no reason to take any time at all.
// It exists so a dead or stale endpoint fails fast: an MCP client waiting on a
// tool call must see a readable error in a few seconds, never a hang.
const requestTimeout = 5 * time.Second

// queryTimeout bounds a call to /query, the one request that can legitimately
// take minutes. A mutating statement waits in the app's Approval Console until
// a human answers it, and this call waits with it — giving up sooner would
// abandon the request exactly when the human is about to approve it, and the
// agent would be told the app was gone. The margin over the gate's own deadline
// covers running the statement afterwards.
//
// It is derived from the gate's deadline rather than restated as a number of
// its own: two constants that had to agree, in two packages, would drift, and
// the drift would only ever show up when a human was slow to answer.
const queryTimeout = guard.ApprovalTimeout + 30*time.Second

// notRunningMessage is the one sentence surfaced for every way the app can be
// unreachable — no token file, a stale one, a refused connection, a rejected
// token. An agent gets the same readable instruction whichever it was; there
// is nothing it can do differently for any of them.
const notRunningMessage = "go-db is not running — open the go-db app, then try again."

var errNotRunning = errors.New(notRunningMessage)

// tokenFilePayload is the on-disk shape of api.json.
type tokenFilePayload struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// queryResponse is the subset of the localhost API's /query response body
// this proxy needs. It mirrors service.QueryResult's JSON tags without
// importing internal/service: the wire contract is the seam, not the Go type.
type queryResponse struct {
	Status       string      `json:"status"`
	Message      string      `json:"message"`
	Columns      []string    `json:"columns,omitempty"`
	Rows         [][]*string `json:"rows,omitempty"`
	Truncated    bool        `json:"truncated"`
	AffectedRows int64       `json:"affected_rows"`
}

// connectResponse mirrors the localhost API's /connect response body.
type connectResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// proxy is the pure stdio↔localhost-API proxy: an HTTP client, the pinned
// Profile name, and nothing else. It owns no connection, no logic, no state
// beyond that — every call re-reads the token file fresh, so an app restart
// with a new port or token never strands it.
type proxy struct {
	configDir string
	profile   string
	client    *http.Client
}

func newProxy(configDir, profileName string) *proxy {
	// The client carries no timeout of its own: how long a call may take is a
	// per-endpoint answer, and do applies it. One shared deadline would have to
	// be the longest one, which would let a dead app hang a health check for
	// two and a half minutes.
	return &proxy{configDir: configDir, profile: profileName, client: &http.Client{}}
}

// endpoint reads api.json fresh. Any failure to read or parse it — missing
// file, garbage content — is reported as the app not running: from the
// proxy's side those are indistinguishable, and both call for the same fix.
func (p *proxy) endpoint() (tokenFilePayload, error) {
	data, err := os.ReadFile(filepath.Join(p.configDir, tokenFileName))
	if err != nil {
		return tokenFilePayload{}, errNotRunning
	}
	var payload tokenFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return tokenFilePayload{}, errNotRunning
	}
	return payload, nil
}

// do sends one request to the localhost API, allowing it timeout to answer,
// and returns its raw body. Anything that keeps the response from arriving — a
// dead port, a refused connection, a rejected token, a body that never
// finishes — collapses to errNotRunning: none of them are actionable by the
// caller beyond "start the app and try again."
func (p *proxy) do(ctx context.Context, timeout time.Duration, method, path string, body any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ep, err := p.endpoint()
	if err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("go-db: encoding request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d%s", ep.Port, path)
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("go-db: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errNotRunning
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errNotRunning
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// A stale token file naming a live but different server on that
		// port looks like this from here: the proxy cannot authenticate,
		// and cannot tell that apart from the app simply not being ours.
		return nil, errNotRunning
	}
	return data, nil
}

// query submits sql on the pinned Profile with Origin "ai", once, with no
// retry logic of its own — runQuery is what callers use. It waits out the
// Approval Console: a mutating statement does not come back until a human has
// decided it or its deadline has passed.
func (p *proxy) query(ctx context.Context, sql string) (queryResponse, error) {
	data, err := p.do(ctx, queryTimeout, http.MethodPost, "/query", map[string]string{
		"profile": p.profile,
		"sql":     sql,
		"origin":  "ai",
	})
	if err != nil {
		return queryResponse{}, err
	}
	var resp queryResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return queryResponse{}, fmt.Errorf("go-db: decoding query response: %w", err)
	}
	return resp, nil
}

// runQuery submits sql on the pinned Profile, lazily connecting it first if
// the gate reports it is not open: one /connect attempt, and if that
// succeeds, one retry of the query. A Profile that fails to connect reports
// its own error text rather than the query's, since the query never ran.
func (p *proxy) runQuery(ctx context.Context, sql string) (queryResponse, error) {
	resp, err := p.query(ctx, sql)
	if err != nil || resp.Status != "not_connected" {
		return resp, err
	}

	data, err := p.do(ctx, requestTimeout, http.MethodPost, "/connect", map[string]string{"profile": p.profile})
	if err != nil {
		return queryResponse{}, err
	}
	var connectResp connectResponse
	if err := json.Unmarshal(data, &connectResp); err != nil {
		return queryResponse{}, fmt.Errorf("go-db: decoding connect response: %w", err)
	}
	if !connectResp.OK {
		return queryResponse{}, errors.New(connectResp.Message)
	}

	return p.query(ctx, sql)
}

// escapeIdentifier doubles embedded backticks so name can be interpolated
// between a pair of them in a statement, the same escaping MySQL itself uses
// for a backtick-quoted identifier.
func escapeIdentifier(name string) string {
	return strings.ReplaceAll(name, "`", "``")
}
