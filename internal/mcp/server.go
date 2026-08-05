package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverVersion is the version this adapter reports in its Implementation.
// go-db has no release process yet, so "dev" is the honest answer.
const serverVersion = "dev"

// NewServer returns an MCP server exposing list_tables, describe_table, and
// run_query, all scoped to profileName and none accepting a profile
// argument — the Profile is pinned at construction, not per call. Every tool
// call proxies through the localhost API whose token file lives in
// configDir, read fresh each time.
//
// NewServer builds the server only; Run wires it to stdio. Tests wire it to
// an in-memory transport instead, which is why configDir is a parameter
// rather than resolved internally.
func NewServer(configDir, profileName string) *sdkmcp.Server {
	p := newProxy(configDir, profileName)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "go-db",
		Version: serverVersion,
	}, &sdkmcp.ServerOptions{
		Instructions: fmt.Sprintf(
			"Every tool here talks to exactly one database: the go-db Profile %q. "+
				"No tool accepts a profile argument — it cannot be pointed anywhere else. "+
				"The go-db app must be running locally for any tool call to succeed.",
			profileName,
		),
	})

	type listTablesInput struct{}
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "list_tables",
		Description: fmt.Sprintf("List the tables in the go-db Profile %q.", profileName),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ listTablesInput) (*sdkmcp.CallToolResult, any, error) {
		resp, err := p.runQuery(ctx, "SHOW TABLES")
		return toolResult(resp, err)
	})

	type describeTableInput struct {
		Table string `json:"table" jsonschema:"the table name to describe"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "describe_table",
		Description: fmt.Sprintf("Describe one table's columns in the go-db Profile %q.", profileName),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in describeTableInput) (*sdkmcp.CallToolResult, any, error) {
		if in.Table == "" {
			return nil, nil, errors.New("table is required")
		}
		resp, err := p.runQuery(ctx, "DESCRIBE `"+escapeIdentifier(in.Table)+"`")
		return toolResult(resp, err)
	})

	type runQueryInput struct {
		SQL string `json:"sql" jsonschema:"the SQL to run against the pinned Profile"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "run_query",
		Description: fmt.Sprintf(
			"Run a read query against the go-db Profile %q. A mutating statement is not "+
				"executed — it is withheld for human approval in the go-db app and this "+
				"call reports that instead of a result.",
			profileName,
		),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runQueryInput) (*sdkmcp.CallToolResult, any, error) {
		resp, err := p.runQuery(ctx, in.SQL)
		return toolResult(resp, err)
	})

	return server
}

// toolResult turns one query's outcome into the MCP result an agent sees.
//
// A transport-level failure (err != nil — the app is not running, or the
// localhost API could not be reached) becomes a tool error: the SDK's
// ToolHandlerFor packs a returned error into CallToolResult.Content with
// IsError set, so the one-sentence message arrives as a proper MCP response,
// never a stack trace.
//
// Past that, the gate's own verdict decides the shape: "ok" renders the rows
// as JSON text; "requires_approval" is not an error at all — it is the
// gate's own sentence explaining that the mutation is withheld, which the
// agent needs to read, not have swallowed as a failure. Everything else
// (not_connected after a failed lazy connect, failed) is a tool error
// carrying the backend's own message.
func toolResult(resp queryResponse, err error) (*sdkmcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}

	switch resp.Status {
	case "ok":
		payload := struct {
			Columns   []string    `json:"columns"`
			Rows      [][]*string `json:"rows"`
			Truncated bool        `json:"truncated"`
			RowCount  int         `json:"row_count"`
		}{
			Columns:   resp.Columns,
			Rows:      resp.Rows,
			Truncated: resp.Truncated,
			RowCount:  len(resp.Rows),
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("go-db: encoding result: %w", err)
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
		}, nil, nil

	case "requires_approval":
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: resp.Message}},
		}, nil, nil

	default:
		return nil, nil, errors.New(resp.Message)
	}
}
