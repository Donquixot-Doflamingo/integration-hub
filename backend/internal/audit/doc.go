// Package audit records the lossless per-request audit trail (events, attempts, final record), applies payload policies (full, masked, sealed, metadata_only) and buffers writes off the hot path (DESIGN §13).
package audit
