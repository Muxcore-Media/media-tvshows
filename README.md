# Media TV Shows

[![CI](https://github.com/Muxcore-Media/media-tvshows/actions/workflows/ci.yml/badge.svg)](https://github.com/Muxcore-Media/media-tvshows/actions)
[![Go Version](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev/)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

**TV show library manager for MuxCore — browse, search, and manage your TV series with TMDB metadata, season/episode tracking, and admin UI integration.**

A MuxCore sidecar module that manages TV show libraries with hierarchical series/season/episode storage, artwork serving, and full MediaAdminService support for the admin UI.

---

## How It Works

```
Admin UI ──→ media-tvshows ──→ SQLite (series, seasons, episodes)
               │
               ▼
         Serves artwork via HTTP /images/
```

### Key Features

- **Series/Season/Episode hierarchy** — full tree structure with foreign key cascades
- **TMDB metadata** — stores TMDB IDs for future metadata refresh integration
- **Per-episode and per-season monitoring** — toggle monitoring at any level; season toggle cascades to episodes
- **Admin UI integration** — implements `MediaAdminService` for browsing, searching, editing, and artwork management
- **Flat MediaAdminService model** — each series appears as a `MediaItem` with metadata fields (season count, episode count, status, network, vote average)
- **TV-specific management API** — `TvManagementService` for add, remove, list, get, refresh, and monitoring operations

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
| `MUXCORE_GRPC_INSECURE` | `false` | Disable TLS for dev |

---

## Quick Start

```bash
# Build
make build

# Run against local core (dev mode)
export MUXCORE_GRPC_INSECURE=true
./media-tvshows --muxcore-mesh-addr localhost:9090
```

---

## Deployment

### Docker

```bash
make docker
docker run -d --restart=unless-stopped \
  -e MUXCORE_GRPC_ADDR=core:9090 \
  -e MUXCORE_GRPC_INSECURE=true \
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

- Registers with capabilities: `"media.library"`
- Implements `contracts-media-admin` `MediaAdminService`
- Exposes TV-specific `TvManagementService` gRPC API
- Uses SQLite for persistence (WAL mode, single connection)
- Serves artwork images via HTTP file server

---

## License

GPL-3.0
