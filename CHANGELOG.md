# Changelog

## [0.1.20] - 2026-10-05


### Security
- Artwork (poster/backdrop) downloads use the netguard UserURL client: private, loopback, link-local and cloud-metadata targets are blocked at dial time and on every redirect (NFR-SEC-009 / RULE-VAL-2; sdk/go/module v0.6.6).
- Root-folder validation fails closed: if the media.roots registry is unreachable, `root_folder_path` (AddTVShow, UpdateTVShow) is refused instead of accepted (NFR-SEC-008 / RULE-VAL-1).
- AddEpisodeFile confines absolute `file_path` to registered tv roots (pathguard: traversal, symlink and sibling-prefix escapes rejected); relative storage keys with `..` are rejected.
- File deletion on RemoveTVShow/RemoveEpisodeFile resolves symlinks (pathguard) and unlinks links rather than following them.

## [0.1.19] - 2026-10-05


### Fixed
- Data race on the core mesh client: `mc` is now an `atomic.Pointer` read through `coreClient()`, written by `dialCore` and swapped out on `Stop`.

## [0.1.18] - 2026-10-05


### Security
- gRPC server and peer dials use mesh TLS (meshtls, sdk/go/module v0.6.5) unless the dev insecure flag is set (ADR-0016/0017).

## [0.1.17] - 2026-10-05

### Changed
- Built on core v0.6.14 / sdk/go/module v0.6.4: unregisters on shutdown and re-registers after core restarts (ADR-0022).

## [0.1.16] - 2026-10-05


### Changed
- Reported version comes from muxcore.json (ADR-0021); built on core v0.6.12 / sdk/go/module v0.6.3 (mesh enrollment, ADR-0017).

## [0.1.15] - 2026-10-05


### Fixed
- `backfillSeriesTitles` no longer writes while its read cursor is open: rows are drained and closed before the title upserts (NFR-REL; avoids SQLITE_BUSY / single-connection deadlock).

## [0.1.14] - 2026-10-05


### Added
- Upgrade test (ADR-0015, NFR-DATA-002, FR-INS-005): `internal/upgrade_test.go` opens a committed `v0.1.9` snapshot (`internal/testdata/upgrade/`) with the current code twice and checks schema superset, column defaults, seeded rows, and integrity. No migration bug found.

### Changed
- Test dependency `core/sdk/go/module` bumped to v0.6.1 (`moduletest`).

## [0.1.13] - 2026-10-05

### Changed
- CI runs on GitHub-hosted runners from the umbrella template; retired-origin workflows removed.
- Dependencies resolve from published GitHub tags (no filesystem `replace`); requires core v0.6.0.

## [0.1.14] — 2026-09-06

### Fixed
- Bump `contracts-media-admin` to Feature enum generation so tip admin-ui Unified Wanted and calendar gates recognize TV `FEATURE_MISSING`, `FEATURE_TAGS`, and `FEATURE_CALENDAR` (umbrella #124).

## [0.1.13] — 2026-08-20

### Added
- `ExportState`/`ImportState` Backupable; advertise `backupable`.
- `RemoveEpisodeFile` accepts `episode_id` when `file_id` is empty (resolves primary episode file).

## [0.1.12] — 2026-08-20

### Added
- `RemoveEpisodeFile` accepts `episode_id` when `file_id` is empty (resolves primary episode file).

## [0.1.11] — 2026-08-20

### Added
- `UpdateMetadata` persists `monitored` (cascades seasons/episodes) and `series_type`.
- Admin MediaItem metadata includes `series_type`.

## [0.1.10] — 2026-08-18

### Fixed
- `ListMissing` excludes season-0 specials so automation does not treat TMDB S00 episodes as wanted.

## [0.1.9] — 2026-08-10

### Added

- Advertise `settings` capability so admin-ui discovers SettingsProvider without ListAll probing.

## [0.1.8] — 2026-08-10

### Added
- SettingsProvider mesh (`RegisterSettings`) for `image_dir` (live artwork path).


## [0.1.7] — 2026-08-10

### Fixed
- Prefer absolute `destination_path` on file import; resolve relative storage keys under series root for `/stream/tv`.


## [0.1.6] — 2026-08-10

### Fixed
- PathUnescape episode stream ids for proxied playback URLs.


## [0.1.5] — 2026-08-10

### Fixed
- Sync Info()/muxcore.json version to **0.1.5**.

## v0.1.0 (2026-06-14)

- Initial release
- Series/season/episode hierarchy with SQLite storage
- `TvManagementService` gRPC API (add, remove, list, get, update, refresh, monitoring, episode files)
- Tags, calendar, missing episodes, alternate titles, and episode lookup
- Series types (`standard` / `daily` / `anime`) and optional `delete_files` on remove
- `MediaAdminService` contract implementation for admin UI (including history, tags, calendar, missing)
- Artwork serving via HTTP file server
- Per-episode and per-season monitoring with season-to-episode cascade
- Consumes `file.imported` and `download.dispatched` events; validates root paths via `media-root-folders`
