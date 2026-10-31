// Command mockvendor is the programmable fake vendor used by tests and local dev (DESIGN §19 M0, contracts/openapi/mockvendor.v1.yaml).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "mockvendor: not implemented yet")
	os.Exit(2)
}
