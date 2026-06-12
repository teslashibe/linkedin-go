// Command linkedin-mcp is a stdio MCP server exposing the linkedin-go tool
// surface to any MCP host (Cursor, Claude Desktop, etc.).
//
// Auth is loaded from ~/.linkedin-mcp/cookies.json. Env vars (LI_AT,
// CSRF_TOKEN, JSESSIONID) take precedence when set. Refresh the file by
// copying fresh cookies from browser DevTools > Application > Cookies on
// linkedin.com.
//
// Config file: ~/.linkedin-mcp/cookies.json
//
//	{
//	  "li_at":        "...",
//	  "csrf_token":   "ajax:...",
//	  "extra_cookies":"bcookie=...; bscookie=...; lidc=...; ..."
//	}
//
// Add to ~/.cursor/mcp.json:
//
//	{"mcpServers":{"linkedin":{"command":"/Users/you/bin/linkedin-mcp"}}}
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	linkedin "github.com/teslashibe/linkedin-go"
	linkmcp "github.com/teslashibe/linkedin-go/mcp"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// cookiesFile holds the fields we persist to ~/.linkedin-mcp/cookies.json.
type cookiesFile struct {
	LiAt         string `json:"li_at"`
	CSRFToken    string `json:"csrf_token"`
	ExtraCookies string `json:"extra_cookies"`
}

// defaultConfigPath returns ~/.linkedin-mcp/cookies.json.
func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".linkedin-mcp", "cookies.json")
}

// loadAuth reads cookies.json then overlays any env vars that are set.
func loadAuth() (linkedin.Auth, error) {
	var cfg cookiesFile

	data, err := os.ReadFile(defaultConfigPath())
	if err != nil && !os.IsNotExist(err) {
		return linkedin.Auth{}, fmt.Errorf("read config: %w", err)
	}
	if data != nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return linkedin.Auth{}, fmt.Errorf("parse config: %w", err)
		}
	}

	// Env vars override file values.
	if v := os.Getenv("LI_AT"); v != "" {
		cfg.LiAt = v
	}
	if v := os.Getenv("CSRF_TOKEN"); v != "" {
		cfg.CSRFToken = v
	}

	if cfg.LiAt == "" || cfg.CSRFToken == "" {
		return linkedin.Auth{}, fmt.Errorf(
			"LinkedIn credentials not found.\n" +
				"Add them to %s or set LI_AT and CSRF_TOKEN env vars.\n" +
				"Get cookies from browser DevTools > Application > Cookies on linkedin.com",
			defaultConfigPath(),
		)
	}

	return linkedin.Auth{
		LiAt:         cfg.LiAt,
		CSRF:         cfg.CSRFToken,
		ExtraCookies: cfg.ExtraCookies,
	}, nil
}

func main() {
	log.SetOutput(os.Stderr)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "linkedin-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	auth, err := loadAuth()
	if err != nil {
		return err
	}

	client := linkedin.New(auth)

	s := server.NewMCPServer(
		"linkedin-mcp",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	provider := linkmcp.Provider{}
	for _, t := range provider.Tools() {
		t := t
		rawSchema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return fmt.Errorf("marshal schema for %s: %w", t.Name, err)
		}
		tool := mcp.NewToolWithRawSchema(t.Name, t.Description, rawSchema)
		s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			raw, err := json.Marshal(req.Params.Arguments)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, invokeErr := t.Invoke(ctx, client, raw)
			if invokeErr != nil {
				return mcp.NewToolResultError(invokeErr.Error()), nil
			}
			out, err := json.Marshal(result)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(out)), nil
		})
	}

	return server.ServeStdio(s)
}
