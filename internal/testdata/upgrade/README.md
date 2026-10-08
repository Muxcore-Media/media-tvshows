# Upgrade snapshots (ADR-0015)

| File | Produced by | Notes |
|------|-------------|-------|
| `v0.1.20.db` | `media-tvshows` tag `v0.1.20` (the release this slice upgrades from, ADR-0015 section 4) | same seed; schema identical to v0.1.9, rows predate `parental_rating*` so every series upgrades as "unavailable" (ADR-0031) |
| `v0.1.20.schema.sql` | `sqlite3 v0.1.20.db .schema` | schema of that tag |
| `v0.1.9.db` | `media-tvshows` tag `v0.1.9` (previous release-train version, and the tag before the latest tag `v0.1.13`) | `Init` of that tag creates the schema; rows seeded by `seed_upgrade_test.go.txt` |
| `v0.1.9.schema.sql` | `sqlite3 v0.1.9.db .schema` | schema of that tag |
| `seed_upgrade_test.go.txt` | seed test (build tag `upgradeseed`), stored as `.txt` so it does not compile here | |

## How it was produced

```sh
git worktree add /tmp/media-tvshows-v0.1.9 v0.1.9
cp internal/testdata/upgrade/seed_upgrade_test.go.txt /tmp/media-tvshows-v0.1.9/internal/seed_upgrade_test.go
cd /tmp/media-tvshows-v0.1.9
# v0.1.9's go.sum no longer matches the republished core v0.5.1; resolve via a
# throwaway modfile (empty go.sum) instead of editing the old worktree.
cp go.mod /tmp/old.mod; : > /tmp/old.sum
GOWORK=off GOSUMDB=off GOFLAGS="-mod=mod -modfile=/tmp/old.mod" \
  UPGRADE_SEED_DB=/tmp/v0.1.9.db go test -tags upgradeseed -run TestUpgradeSeed ./internal/
sqlite3 /tmp/v0.1.9.db 'PRAGMA journal_mode=DELETE; PRAGMA page_size=1024; VACUUM;'
sqlite3 /tmp/v0.1.9.db .schema > v0.1.9.schema.sql
```

The database is stored in rollback-journal mode (single file); the module
switches it to WAL on open.

`v0.1.20.db` was produced with `scripts/upgrade-fixtures/snapshot.sh <module> v0.1.20 internal`.

## Seed summary

| Table | Rows | Notes |
|-------|------|-------|
| `series` | 2 | standard + anime, one unmonitored, root folder paths (`/home/alice/anime`) |
| `seasons` | 3 | one unmonitored |
| `episodes` | 4 | with and without files |
| `episode_files` | 2 | absolute personal file paths, 2 GiB size |
| `tags` / `item_tags` | 2 / 3 | |
| `history` | 3 | grab / import / delete_file; indexer, download id, file paths, `data_json` |
| `series_titles` | 4 | primary, original, alias |

`internal/upgrade_test.go` opens a copy twice with the current code and checks
schema superset, column defaults, seeded rows via the store API, and integrity.
