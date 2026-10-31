// Command hubmcp is the MCP server that lets Hub users discover, dry-run and invoke integrations from an AI assistant (F-20, contracts/mcp/hubmcp-tools.json).
//
// It talks to the Hub only through the contracted HTTP API, never internal/.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "hubmcp: not implemented yet")
	os.Exit(2)
}
