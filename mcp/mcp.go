// Package mcp exposes the linkedin-go [linkedin.Client] surface as a set of
// MCP (Model Context Protocol) tools that any host application can mount on
// its own MCP server.
//
// All tools wrap exported methods on *linkedin.Client. Each tool is defined
// via [mcptool.Define] so the JSON input schema is reflected from the typed
// input struct — no hand-maintained schemas, no drift.
//
// Usage from a host application:
//
//	import (
//	    "github.com/teslashibe/mcptool"
//	    linkedin "github.com/teslashibe/linkedin-go"
//	    linkmcp "github.com/teslashibe/linkedin-go/mcp"
//	)
//
//	client := linkedin.New(linkedin.Auth{...})
//	for _, tool := range linkmcp.Provider{}.Tools() {
//	    // register tool with your MCP server, passing client as the client arg
//	    // when invoking
//	}
//
// The [Excluded] map documents methods on *Client that are intentionally not
// exposed via MCP, with a one-line reason. The coverage test in mcp_test.go
// fails if a new exported method is added without either being wrapped by a
// tool or appearing in [Excluded].
package mcp

import "github.com/teslashibe/mcptool"

// Provider implements [mcptool.Provider] for linkedin-go. The zero value is
// ready to use.
type Provider struct{}

// Platform returns "linkedin".
func (Provider) Platform() string { return "linkedin" }

// Tools returns every linkedin-go MCP tool, in registration order.
func (Provider) Tools() []mcptool.Tool {
	out := make([]mcptool.Tool, 0, len(searchTools)+len(profileTools)+len(groupTools)+len(messagingTools)+len(resolveTools))
	out = append(out, searchTools...)
	out = append(out, profileTools...)
	out = append(out, groupTools...)
	out = append(out, messagingTools...)
	out = append(out, resolveTools...)
	return out
}
