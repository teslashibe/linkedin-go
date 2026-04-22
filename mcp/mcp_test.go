package mcp_test

import (
	"reflect"
	"testing"

	linkedin "github.com/teslashibe/linkedin-go"
	linkmcp "github.com/teslashibe/linkedin-go/mcp"
	"github.com/teslashibe/mcptool"
)

// TestEveryClientMethodIsWrappedOrExcluded fails when a new exported method
// is added to *linkedin.Client without either being wrapped by an MCP tool
// or being added to linkmcp.Excluded with a reason. This is the drift-
// prevention mechanism: keeping the MCP surface in lockstep with the package
// API is enforced by CI rather than convention.
func TestEveryClientMethodIsWrappedOrExcluded(t *testing.T) {
	rep := mcptool.Coverage(
		reflect.TypeOf(&linkedin.Client{}),
		linkmcp.Provider{}.Tools(),
		linkmcp.Excluded,
	)
	if len(rep.Missing) > 0 {
		t.Fatalf("methods missing MCP exposure (add a tool or list in excluded.go): %v", rep.Missing)
	}
	if len(rep.UnknownExclusions) > 0 {
		t.Fatalf("excluded.go references methods that don't exist on *Client (rename?): %v", rep.UnknownExclusions)
	}
	if len(rep.Wrapped)+len(rep.Excluded) == 0 {
		t.Fatal("no wrapped or excluded methods detected — coverage helper is mis-configured")
	}
}

// TestToolsValidate verifies every tool has a non-empty name in canonical
// snake_case form, a description within length limits, and a non-nil Invoke
// + InputSchema.
func TestToolsValidate(t *testing.T) {
	if err := mcptool.ValidateTools(linkmcp.Provider{}.Tools()); err != nil {
		t.Fatal(err)
	}
}

// TestPlatformName guards against accidental rebrands.
func TestPlatformName(t *testing.T) {
	if got := (linkmcp.Provider{}).Platform(); got != "linkedin" {
		t.Errorf("Platform() = %q, want linkedin", got)
	}
}

// TestToolsHaveLinkedinPrefix encodes the per-platform naming convention.
func TestToolsHaveLinkedinPrefix(t *testing.T) {
	for _, tool := range (linkmcp.Provider{}).Tools() {
		if len(tool.Name) < len("linkedin_") || tool.Name[:len("linkedin_")] != "linkedin_" {
			t.Errorf("tool %q lacks linkedin_ prefix", tool.Name)
		}
	}
}
