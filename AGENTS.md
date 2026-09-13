# AGENTS.md

Single Go module (`github.com/vigovlugt/imchlite`, Go 1.26, cgo required for mattn/go-sqlite3) with an embedded React frontend.

## Build order matters

`static.go` uses `//go:embed all:frontend/dist`, so `go build .` fails if the frontend hasn't been built:

```
cd frontend && bun run build && cd ..
go build .
```

`scripts/build-linux.sh` does exactly this.

## Commands

- Frontend dev: `bun run dev` in `frontend/` (Vite proxies `/api` to `127.0.0.1:3000`, so run the Go backend alongside).
- Frontend routes: TanStack Router file-based routes in `frontend/src/routes/`; `routeTree.gen.ts` is generated (by the Vite plugin on dev/build, or `bun run generate-routes`) — never edit it by hand.
- Run app: `go run . --library-location <dir>` — listens on `127.0.0.1:3000` and opens a browser. The library dir gets a `.imchlite/` folder (SQLite db + thumbnails).
- Tests: `go test ./...` (only `ffmpeg/` has tests).

## Embedded binaries

`ffmpeg/` and `exiftool/` embed the actual ffmpeg/exiftool distributions per-platform (`embed_linux_amd64.go`, `embed_windows_amd64.go`; `embed_other.go` embeds nothing, so cross-compiling to other GOOS/GOARCH yields a runtime error). `bin/` dirs are large; extraction to a temp dir happens at app startup. Linux bins run via CGO-free extraction — no system ffmpeg/exiftool needed.

## Layout

All backend code is flat in root `package main` (indexer, processor, repos, migrations, api). `testdir/` and `testdir2/` are local sample libraries used for manual testing (gitignored).
