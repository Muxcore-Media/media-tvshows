package internal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
)

func (m *Module) configureDatabase(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable foreign_keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS series (
			id            TEXT PRIMARY KEY,
			tmdb_id       INTEGER UNIQUE,
			name          TEXT NOT NULL,
			original_name TEXT DEFAULT '',
			year          INTEGER DEFAULT 0,
			overview      TEXT DEFAULT '',
			tagline       TEXT DEFAULT '',
			status        TEXT DEFAULT '',
			network       TEXT DEFAULT '',
			first_air_date TEXT DEFAULT '',
			last_air_date TEXT DEFAULT '',
			vote_average  REAL DEFAULT 0,
			genres        TEXT DEFAULT '[]',
			poster_path   TEXT DEFAULT '',
			backdrop_path TEXT DEFAULT '',
			monitored     INTEGER DEFAULT 1,
			total_seasons   INTEGER DEFAULT 0,
			total_episodes  INTEGER DEFAULT 0,
			quality_profile_id TEXT DEFAULT '',
			root_folder_path   TEXT DEFAULT '',
			series_type   TEXT DEFAULT 'standard',
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create series table: %w", err)
	}
	for _, col := range []string{
		`ALTER TABLE series ADD COLUMN quality_profile_id TEXT DEFAULT ''`,
		`ALTER TABLE series ADD COLUMN root_folder_path TEXT DEFAULT ''`,
		`ALTER TABLE series ADD COLUMN series_type TEXT DEFAULT 'standard'`,
	} {
		if _, err := db.ExecContext(ctx, col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate series: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS seasons (
			id            TEXT PRIMARY KEY,
			series_id     TEXT NOT NULL,
			season_number INTEGER NOT NULL,
			name          TEXT DEFAULT '',
			overview      TEXT DEFAULT '',
			episode_count  INTEGER DEFAULT 0,
			air_date      TEXT DEFAULT '',
			poster_path   TEXT DEFAULT '',
			monitored     INTEGER DEFAULT 1,
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL,
			FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create seasons table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS episodes (
			id              TEXT PRIMARY KEY,
			series_id       TEXT NOT NULL,
			season_id       TEXT NOT NULL,
			tmdb_id         INTEGER DEFAULT 0,
			episode_number  INTEGER NOT NULL,
			season_number   INTEGER NOT NULL,
			absolute_number INTEGER DEFAULT 0,
			name            TEXT DEFAULT '',
			overview        TEXT DEFAULT '',
			air_date        TEXT DEFAULT '',
			still_path      TEXT DEFAULT '',
			monitored       INTEGER DEFAULT 1,
			has_file        INTEGER DEFAULT 0,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE,
			FOREIGN KEY (season_id) REFERENCES seasons(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create episodes table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_series_name ON series(name)
	`); err != nil {
		return fmt.Errorf("create series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episodes_series ON episodes(series_id)
	`); err != nil {
		return fmt.Errorf("create episodes series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episodes_season ON episodes(season_id)
	`); err != nil {
		return fmt.Errorf("create episodes season index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_seasons_series ON seasons(series_id)
	`); err != nil {
		return fmt.Errorf("create seasons series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS episode_files (
			id         TEXT PRIMARY KEY,
			episode_id TEXT NOT NULL,
			file_path  TEXT NOT NULL,
			quality    TEXT DEFAULT '',
			size_bytes INTEGER DEFAULT 0,
			container  TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create episode_files table: %w", err)
	}
	if err := migrateEpisodeFilesForeignKey(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episode_files_ep ON episode_files(episode_id)
	`); err != nil {
		return fmt.Errorf("create episode files index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS tags (
			id TEXT PRIMARY KEY,
			label TEXT UNIQUE NOT NULL,
			created_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create tags table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS item_tags (
			item_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			PRIMARY KEY (item_id, tag_id)
		)
	`); err != nil {
		return fmt.Errorf("create item_tags table: %w", err)
	}

	prevDB := m.db
	m.db = db
	if err := m.ensureHistoryTable(ctx); err != nil {
		m.db = prevDB
		return err
	}
	if err := m.ensureSeriesTitlesTable(ctx); err != nil {
		m.db = prevDB
		return err
	}
	m.backfillSeriesTitles(ctx)
	m.db = prevDB
	return nil
}

func migrateEpisodeFilesForeignKey(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(episode_files)`)
	if err != nil {
		return fmt.Errorf("inspect episode_files foreign keys: %w", err)
	}
	hasFK := false
	for rows.Next() {
		var id, seq, table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan foreign_key_list: %w", err)
		}
		if table == "episodes" && from == "episode_id" {
			hasFK = true
			break
		}
	}
	_ = rows.Close()
	if hasFK {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin episode_files migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS episode_files_new (
			id         TEXT PRIMARY KEY,
			episode_id TEXT NOT NULL,
			file_path  TEXT NOT NULL,
			quality    TEXT DEFAULT '',
			size_bytes INTEGER DEFAULT 0,
			container  TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
		)
	`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create episode_files_new: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO episode_files_new (id, episode_id, file_path, quality, size_bytes, container, created_at)
		SELECT id, episode_id, file_path, quality, size_bytes, container, created_at FROM episode_files
	`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("copy episode_files: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE episode_files`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("drop episode_files: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE episode_files_new RENAME TO episode_files`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("rename episode_files_new: %w", err)
	}
	return tx.Commit()
}

func networkFromTVDetails(networks []*metadatav1.Network) string {
	for _, n := range networks {
		if n == nil {
			continue
		}
		if name := strings.TrimSpace(n.GetName()); name != "" {
			return name
		}
	}
	return ""
}
