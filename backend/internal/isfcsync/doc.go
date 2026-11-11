// Package isfcsync reconciles the Hub's tenants (and, HUB-483, its ISFC caller keys) with ISFC
// (AUTH_PLAN §2.1, §6.2, F-25). ISFC owns the tenant; the Hub keeps a read-only copy plus its own
// settings, and this package is the only writer of the copy.
//
// A reconcile always pulls the full current state from ISFC, so a lost, duplicated or reordered
// event only costs one extra run. It never deletes a tenant, never disables anything because a
// fetch failed (ISFC down keeps the last state), and writes every change through
// control.Publisher.Edit with actor "isfc-sync", so a change reaches the change log and a new
// snapshot like any admin edit. Plan is the pure core: it decides, Reconciler applies.
package isfcsync
