# Media TV Shows

[![CI](https://github.com/Muxcore-Media/media-tvshows/actions/workflows/ci.yml/badge.svg)](https://github.com/Muxcore-Media/media-tvshows/actions)
[![Go Version](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev/)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

**TV show library manager for MuxCore — browse, search, and manage your TV series with TMDB metadata, season/episode tracking, and admin UI integration.**

A MuxCore sidecar module that manages TV show libraries with hierarchical series/season/episode storage, artwork serving, and full MediaAdminService support for the admin UI.

---

## How It Works

```
Admin UI / clients ──→ media-tvshows ──→ SQLite (series, seasons, episodes, tags, history)
                         │
                         ├──→ metadata-tmdb (search / refresh)
                         ├──→ media-root-folders (root path validation)
                         ├──← file.imported / download.dispatched events
                         └──→ HTTP /images/ (artwork)
```

### Key Features

- **Series/Season/Episode hierarchy** — full tree structure with foreign key cascades
- **TMDB metadata** — import and refresh via the `metadata` capability (`metadata-tmdb`)
- **Episode file tracking** — add/remove episode files; optional on-disk delete; multi-episode file links
- **Series types** — `standard`, `daily`, or `anime` (affects lookup / absolute numbering)
- **Per-episode and per-season monitoring** — toggle monitoring at any level; season toggle cascades to episodes
- **Tags & calendar** — tag series; air-date calendar; list monitored missing episodes
- **Alternate titles** — store and look up series by alternate / clean titles
- **History** — import, grab, and delete events via `MediaAdminService.ListHistory`
- **Root folder paths** — validates against `media-root-folders` when available
- **Event consumers** — subscribes to `file.imported` and `download.dispatched`
- **Admin UI integration** — implements `MediaAdminService` (browse, search, edit, artwork, tags, calendar, missing, history)
- **Flat MediaAdminService model** — each series appears as a `MediaItem` with metadata fields (season count, episode count, status, network, vote average)
- **TV-specific management API** — `TvManagementService` for library CRUD, monitoring, files, tags, calendar, titles, and lookup

---

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--muxcore-mesh-addr` | - | Core gRPC address |
| `--muxcore-module-id` | `media-tvshows` | Module identity |

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TVSHOWS_DB_PATH` | `/var/lib/media-tvshows/tvshows.db` | SQLite database path |
| `TVSHOWS_GRPC_ADDR` | `:9440` | gRPC listen address |
| `TVSHOWS_HTTP_ADDR` | `:9450` | HTTP listen address for artwork |
| `TVSHOWS_IMAGE_DIR` | `/var/lib/media-tvshows/images` | Artwork images directory |
| `MUXCORE_GRPC_ADDR` | `localhost:9090` | Core mesh gRPC address |
| `MUXCORE_INSECURE_DISABLE_TLS` | `false` | Disable TLS for dev |
| `MUXCORE_MODULE_ID` | `media-tvshows` | Module identity (overrides flag default) |

---

## Quick Start

```bash
# Build
make build

# Run against local core (dev mode)
export MUXCORE_INSECURE_DISABLE_TLS=true
./media-tvshows --muxcore-mesh-addr localhost:9090
```

---

## Deployment

### Docker

```bash
make docker
docker run -d --restart=unless-stopped \
  -e MUXCORE_GRPC_ADDR=core:9090 \
  -e MUXCORE_INSECURE_DISABLE_TLS=true \
  ghcr.io/muxcore-media/media-tvshows:latest
```

### docker-compose

```bash
docker compose -f deploy/docker-compose.yml up
```

### systemd

```bash
sudo cp deploy/systemd/muxcore-module.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now muxcore-module
```

---

## Development

```bash
make test     # run tests with race detection
make lint     # golangci-lint
make fmt      # format code
make proto    # regenerate protobuf code
```

### Integration Tests

```bash
# Start core in dev mode, then:
MUXCORE_GRPC_ADDR=localhost:9090 go test -tags=integration -race -count=1 ./test/
```

---

## Implementation

- Registers with capabilities: `"media.library"`, `"media.library.tv"`
- Implements `contracts-media-admin` `MediaAdminService` (including tags, calendar, missing, history; collections unimplemented)
- Exposes `TvManagementService` gRPC API (`proto/tvmgmtv1`)
- Uses SQLite for persistence (WAL mode, single connection)
- Serves artwork images via HTTP file server
- Discovers `metadata` and `media-root-folders` peers over the core mesh

---

## License

GPL-3.0
