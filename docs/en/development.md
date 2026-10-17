# Development

## Requirements

- **Go 1.26+** (`go.mod` says `go 1.26.0`; the project was built and tested on Go 1.27).
- CGO is **not needed**: SQLite is pure Go (`modernc.org/sqlite`). The exception is tests with the race
  detector (`-race`), which need `gcc`.
- To check the UI: Python 3 with Playwright and Chromium (optional).
- `shellcheck` for scripts (optional).

## Building

```bash
go build ./cmd/dyrokol            # note: writes the file ./dyrokol into the current directory
go build -o /tmp/dyrokol ./cmd/dyrokol
```

All platforms at once:

```bash
scripts/build.sh                  # all targets
scripts/build.sh linux-amd64      # one or more targets by name
```

| Target | GOOS / GOARCH |
| --- | --- |
| `linux-amd64` | linux / amd64 |
| `linux-arm64` | linux / arm64 |
| `linux-arm` | linux / arm (GOARM=7) |
| `termux-arm64` | linux / arm64 |
| `termux-arm` | linux / arm (GOARM=7) |
| `windows-amd64` | windows / amd64 |
| `windows-arm64` | windows / arm64 |

Builds are static (`CGO_ENABLED=0`), `-trimpath`, without debug information (`-s -w`); each weighs
12–13 MB. The UI is embedded into the binary through `go:embed`, so **after editing files in
`internal/server/web` the binary must be rebuilt**.

`cmd/dyrokol/rsrc_windows_*.syso` are the Windows resources (the icon), picked up automatically when
building for Windows.

The Windows service (`dyrokol service ...`) lives in `cmd/dyrokol/service_windows.go`; on other platforms
the command reports that it is not supported (`service_other.go`). You can check that the code builds
for Windows from Linux: `GOOS=windows go vet ./cmd/...`.

Binaries are **not stored in the repository** (`bin/` and `dist/` are in `.gitignore`): they are
published as GitHub Release files, see [Releasing](#releasing).

## Tests

```bash
go vet ./...
go test ./...                       # all packages
CGO_ENABLED=1 go test -race ./...   # with the race detector (needs gcc)
go test -run TestEndToEnd ./internal/server/
```

The bulk is in `internal/server` (end-to-end scenarios on real relays and agents inside the test
process) and `internal/store`.

| Package | What is covered |
| --- | --- |
| `internal/server` | The end-to-end scenario (registration, ports, route, statistics, restart and automatic unlock, deletion); ingress on the relay + egress on an agent; egress on a second machine; SOCKS5; token and `Origin` protection; the source filter; backup and restore; **the port ownership check in a route stream**; counter accuracy; log events; one port on different machines; the route cache; several ingress ports to one egress port `PUT /api/endpoints/{id}/routes`, relay port number change with rollback, `PATCH /api/routes/{id}`, switching a route on and off, the diagram layout API. |
| `internal/store` | Encryption and absence of plaintext, round-trip, cascading deletion, last-admin protection, validation, key rotation, port uniqueness per machine, backup/restore, a source filter without ports, atomic replacement of an egress port's ingress set (`SetEndpointRoutes`), editing a port number and an egress target, `UpdateRoute`, the diagram layout (checks, saving, backup with id reassignment). |
| `internal/agent` | Port identifiers of different relays do not collide, moving a port, rejection of a bad push, counter drain in a report. |
| `internal/traffic` | Counter and peer deltas, traffic accounting in `Relay`, `ConnSet.Shut`. |
| `internal/filter` | allow/deny semantics, pattern parsing, `ValidateSource`. |
| `internal/config` | The data directory and value precedence. |

Tests use free ports on `127.0.0.1` and temporary directories, so they are safe to run in parallel with
your work.

## UI development rig

`scripts/devrig.sh` starts a relay, an admin agent and a test web server, and fills them with demo data
(several machines, ports, egress ports and routes):

```bash
go build -o /tmp/dyrokol ./cmd/dyrokol
scripts/devrig.sh /tmp/dyrokol        # admin UI: http://127.0.0.1:19180, token: uitoken
scripts/devrig.sh stop
```

Since the UI is embedded in the binary, rebuild and restart the rig after edits.

Automated UI checks (need the Python package `playwright` and `/usr/bin/chromium`):

```bash
python3 scripts/uiflows.py            # scenarios: sign-in, diagram, creating a port, duplicate, routes on the Routes page
                                      # (including several ingress ports to one egress port), editing port numbers, saving the diagram layout
                                      # (dragging, frame, label, layers), zoom/pan/full screen, deletion, lock
python3 scripts/uishots.py /tmp/shots # screenshots of all pages: dark, light, mobile
python3 scripts/docshots.py           # regenerate the documentation screenshots (docs/img/)
```

`uishots.py` prints browser console errors and page exceptions, if there were any.

## Project layout

```
cmd/dyrokol/             CLI (main.go), the Windows service (service*.go), Windows resources
internal/
  agent/                 agent: agent.go, ingress.go, admin.go, config.go
  server/                relay: server.go, handshake.go, session.go, ingress.go, push.go,
                         flush.go, auth.go, api*.go; web/ is the embedded UI
  store/                 sealed.go, store.go, model.go, mesh.go, runtime.go, backup.go
  protocol/ transport/ traffic/ copyio/ egress/ socks5/ filter/ config/
docs/                    documentation: ru/ and en/, screenshots in img/
scripts/                 build.sh, release.sh, install.sh, devrig.sh, uiflows.py, uishots.py, docshots.py
assets/                  the application icon
bin/, dist/              build output and release files (in .gitignore)
```

See [Architecture](architecture.md) for details.

## Conventions

- `gofmt` and `go vet` must be clean; scripts pass `shellcheck`.
- Comments explain **why**, not retell the code; every package has a doc comment.
- Storage errors are typed (`store.Error`), and the HTTP status is chosen in one place
  (`writeStoreError`).
- Any configuration change goes through `store.mutate` and then `server.configChanged()`.
- Put new tests for found defects next to the fix (`regress_test.go`).

## How to add…

### …a field or entity to the configuration

1. Schema and model: `store/store.go` (`configSchema`), `store/model.go` (type, scanner, `normalize`).
2. Operations: `store/mesh.go`. They change the configuration only inside `s.mutate`.
3. Backup: `store/backup.go` (`Snapshot`, `Import`, `validate`).
4. **Bump `schemaVersion`** (there are no migrations); the version in the backup snapshot updates itself.
5. Delivery to agents: `server/push.go` (`buildConfig`) and `protocol.ConfigPush`; if the format
   changes, bump `protocol.Version`.
6. API and UI (below), tests, documentation.

### …an admin API endpoint

1. A method in `server/api_mesh.go` (changes) or `api_view.go` (reads) on the `*api` type.
2. Register it in the `adminHandler` table (`server/api.go`).
3. Errors go through `writeStoreError`; the body through `decode` (it rejects unknown fields).
4. After a change call `a.configChanged()`.
5. The client in `web/js/lib/api.js`, the description in `docs/en/API.md`, a test in `internal/server`.

### …a UI page

1. A file in `web/js/pages/`, a component `XxxPage`; data comes from `useApp()` (the shared snapshot) or
   `usePolled(...)` (own polling).
2. Add it to the `PAGES` list in `web/js/shell.js` (id, label, icon).
3. New icons go to `web/js/ui/icons.js`; shared elements to `web/js/ui/kit.js`.
4. Data changes only through `mutate` or `useAction`.
5. Rebuild the binary, check with the rig and `uishots.py`.

### …an egress port type

`protocol.Type*` → validation in `store/model.go` (`Endpoint.normalize`) → execution in
`egress/egress.go` → fields in the UI (`pages/egress.js`) → the API reference.

## Releasing

1. `gofmt -l .` is empty, `go vet ./...` is clean, `CGO_ENABLED=1 go test -race ./...` is green.
2. `python3 scripts/uiflows.py` passes on a fresh rig.
3. Update `protocol.AppVersion` (and `protocol.Version`/`schemaVersion` on incompatible changes).
4. `scripts/release.sh`: builds all targets and lays out in `dist/` files named
   `dyrokol-<os>-<arch>[.exe]` plus `SHA256SUMS`. These are the names `scripts/install.sh` expects.
5. Create a tag (`git tag v1.0.0 && git push --tags`) and a GitHub release, and attach every file from `dist/`.
6. Check that `README.md` and `docs/ru`, `docs/en` match the changes.
