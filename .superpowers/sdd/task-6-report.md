# Task 6 Report — Engine seam `internal/proxyengine`

## Status: DONE

A thin, documented engine boundary is in place. xray-core remains the only
backend; a future backend plugs in by satisfying `Engine`.

## Files
- `internal/proxyengine/engine.go` — `Profile{URI string}`, `Engine` interface
  (`BuildConfig`, `Validate`), `func Default() Engine` returning `*XrayEngine`.
  Package doc states the import direction / cycle-avoidance rationale.
- `internal/proxyengine/xray.go` — `XrayEngine` implementing the interface;
  `buildXrayConfig` scheme-dispatch helper.
- `internal/proxyengine/engine_test.go` — table test `TestXrayEngineBuildConfig`
  (hy2 + vless), unsupported-scheme test, and a `fakeEngine` double proving the
  interface is small enough to swap.

No existing files were modified. `xray.generateProfileConfig` and
`xray.ValidateProfile` are untouched; existing callers keep working unchanged.

## Wiring choice: (b)
`XrayEngine.BuildConfig` scheme-dispatches the URI itself and calls the protocol
builders (`vless.Parse`/`GenerateConfig`, `hysteria2.Parse`/`GenerateConfig`)
directly, then marshals the neutral `xrayconf.XrayConfig` with
`json.MarshalIndent(cfg, "", "  ")` (two-space indent, matching what
`AddProfile` writes). `XrayEngine.Validate` delegates to `xray.ValidateProfile`.

### Why no cycle
The single cross-package edge into xray is one-way: `proxyengine → xray` (for
`ValidateProfile` only). `internal/xray` does **not** import `proxyengine`
(verified: `grep -rn proxyengine internal/xray/` → none). `generateProfileConfig`
was deliberately left in `xray` rather than re-routed through the engine, so
there is no `xray ↔ proxyengine` back-edge. `proxyengine`'s in-module imports
are: `config`, `hysteria2`, `vless`, `xray`, `xrayconf` (all leaf/lower layers).
`go build ./...` is clean — proof there is no cycle.

The scheme switch in `buildXrayConfig` intentionally mirrors
`xray.generateProfileConfig` rather than sharing the unexported helper; this
small, documented duplication is the price of keeping the import edge one-way
(the alternative — exporting/re-routing through xray — risked the cycle and
caller churn the brief told us to avoid).

Note: `proxyengine.buildXrayConfig` does not emit the hysteria2 `insecure`
deprecation warning that `xray.generateProfileConfig` prints (that warning is a
user-facing concern of the `add` command path, not of config generation); the
produced config bytes are otherwise shape-identical.

## TDD evidence
- **RED**: `go test ./internal/proxyengine/` →
  `no non-test Go files ... [build failed]` (interface/Default undefined before
  implementation).
- **GREEN** (after engine.go + xray.go):
  ```
  --- PASS: TestXrayEngineBuildConfig (0.00s)
      --- PASS: TestXrayEngineBuildConfig/hysteria2 (0.00s)
      --- PASS: TestXrayEngineBuildConfig/vless (0.00s)
  --- PASS: TestXrayEngineBuildConfigUnsupported (0.00s)
  --- PASS: TestFakeEngineSatisfiesInterface (0.00s)
  ok  github.com/rtxnik/workspace-cli/internal/proxyengine
  ```

## Acceptance gate (all green)
- `go build ./...` — clean (no cycle)
- `go vet ./...` — clean
- `go test ./...` — all packages pass, including `internal/proxyengine`
- `golangci-lint run` — 0 issues

## Scope
Kept thin per the brief: one interface, one `XrayEngine`, one test fake. No
`Probe` (deferred to Task 7), no registries, no plugin loaders. Existing callers
NOT re-wired through the engine (left as-is to avoid churn/cycle risk; the brief
permits adding the seam only).

## Concerns
- Minor, accepted: the scheme switch is duplicated between
  `proxyengine.buildXrayConfig` and `xray.generateProfileConfig`. When a caller
  is eventually routed through the engine (a later task), one of the two should
  become the single dispatch site. Documented inline in both the package doc and
  `XrayEngine` doc.

---

## Fix — URI dispatch converged (review follow-up)

### Change
`xray.generateProfileConfig` exported as `GenerateProfileConfig`; `AddProfile`
updated to the new name. `proxyengine.buildXrayConfig` deleted; `BuildConfig` now
calls `xray.GenerateProfileConfig(p.URI)` directly. Imports `hysteria2`, `vless`,
`xrayconf`, and `strings` dropped from `proxyengine/xray.go` (no longer needed).

### No-cycle confirmation
```
grep -rn "internal/proxyengine" internal/xray/
exit: 1   (no matches — xray does not import proxyengine)
```

### Covering-test command + result
```
go test ./internal/proxyengine/ ./internal/xray/ -v -run TestXrayEngineBuildConfig
```
```
--- PASS: TestXrayEngineBuildConfig/hysteria2 (0.00s)
--- PASS: TestXrayEngineBuildConfig/vless (0.00s)
--- PASS: TestXrayEngineBuildConfig (0.00s)
ok  github.com/rtxnik/workspace-cli/internal/proxyengine
ok  github.com/rtxnik/workspace-cli/internal/xray
```

### Full gate
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — all packages pass
- `golangci-lint run` — 0 issues
