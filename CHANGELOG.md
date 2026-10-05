# Changelog

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
