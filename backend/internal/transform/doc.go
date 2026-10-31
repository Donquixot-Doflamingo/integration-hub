// Package transform compiles and runs the declarative transformer DSL: canonical request → vendor request, vendor response → canonical response, and error mapping (DESIGN §7).
//
// It is pure: no network, disk, database or wall-clock access.
package transform
