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
- Run app: `go run . --library-dir <dir> [--data-dir <dir>]` — listens on `127.0.0.1:3000` and opens a browser. The SQLite db + thumbnails go in the data dir, which defaults to `<library-dir>/.imchlite/`.
- Index without serving: `go run . --library-dir <dir> --index-only` — indexes and processes all assets, then exits once the task queue is idle (no server, no browser; exits 1 if the library walk failed). `--serve-only` is the opposite: serve without indexing.
- Tests: `go test ./...` (`internal/clients/ffmpeg/` and `ai/` have tests; `ai/` needs onnxruntime, see below).

## NixOS

On NixOS the onnxruntime shared library fails to load under a plain shell (`libstdc++.so.6: cannot open shared object file`). Run anything that initializes onnxruntime through `steam-run`, e.g.:

```
steam-run go test ./...
```

## Embedded binaries

`internal/clients/ffmpeg/` and `internal/clients/exiftool/` embed the actual ffmpeg/exiftool distributions per-platform (`embed_linux_amd64.go`, `embed_windows_amd64.go`; `embed_other.go` embeds nothing, so cross-compiling to other GOOS/GOARCH yields a runtime error). `bin/` dirs are large; on first run they are extracted atomically (staging dir + rename, see `cachedir/`) to `<user cache dir>/imchlite/ffmpeg/` and `.../exiftool/`, where they persist between runs. Linux bins run via CGO-free extraction — no system ffmpeg/exiftool needed.

### libtokenizers

The textual encoder links `github.com/daulet/tokenizers` via cgo, which needs a prebuilt static `libtokenizers.a`. Upstream ships no Windows build, so both archives are committed (LFS) per platform:

- `internal/clients/tokenizers/lib/linux_amd64/libtokenizers.a`
- `internal/clients/tokenizers/lib/windows_amd64/libtokenizers.a`

`internal/ai/cgo_linux_amd64.go` / `cgo_windows_amd64.go` add the matching `-L` for `-ltokenizers` (the Rust build product must be named `libtokenizers.a`, not `libtokenizers_ffi.a`). Windows links with mingw (`-lstdc++ -lws2_32 -luserenv`, already declared upstream), so the Windows build needs a mingw gcc (`x86_64-w64-mingw32-gcc`) on PATH. To rebuild an archive, build `crates/tokenizers` from the pinned module version for the target (e.g. `x86_64-pc-windows-gnu`) and copy the resulting staticlib over the committed file.

## Layout

Backend packages live in `internal/`. Go wrappers around external libraries and tools (exiftool, ffmpeg, hfmodel, onnxruntime, tokenizers, vec1) go in `internal/clients/`. Processing lives in `internal/tasks/`: `process.go` holds the queue, priorities and worker loop shared by all tasks, and each task type has its own file (`index.go`, `file.go`, `metadata.go`, `asset.go`, `thumbnail.go`, `clip.go`). `tests/` is a small committed sample library for manual testing (`go run . --library-dir tests`).
