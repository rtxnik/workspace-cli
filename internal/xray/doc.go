// Package xray manages xray-core VLESS profile lifecycle for the dev-proxy
// container: add/list/use/rm/show/current/regenerate profiles, validate via
// `xray run -test`, atomic symlink swap, transparent migration of legacy
// regular-file config.json.
//
// Phase 22 (CONTEXT.md D-11): this package is intentionally separate from
// internal/profile/ (devpod workspace profiles). The two validators have
// different rules and reserved-name sets; do NOT import or share.
package xray
