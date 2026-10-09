CREATE TABLE series (
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
			updated_at    TEXT NOT NULL,
			parental_rating        TEXT NOT NULL DEFAULT '',
			parental_rating_source TEXT NOT NULL DEFAULT ''
		);
CREATE TABLE seasons (
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
		);
CREATE TABLE episodes (
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
		);
CREATE TABLE episode_files (
			id         TEXT PRIMARY KEY,
			episode_id TEXT NOT NULL,
			file_path  TEXT NOT NULL,
			quality    TEXT DEFAULT '',
			size_bytes INTEGER DEFAULT 0,
			container  TEXT DEFAULT '',
			created_at TEXT NOT NULL
		);
CREATE TABLE tags (
			id TEXT PRIMARY KEY,
			label TEXT UNIQUE NOT NULL,
			created_at TEXT NOT NULL
		);
CREATE TABLE item_tags (
			item_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			PRIMARY KEY (item_id, tag_id)
		);
CREATE TABLE history (
			id           TEXT PRIMARY KEY,
			event_type   TEXT NOT NULL,
			item_id      TEXT NOT NULL,
			title        TEXT NOT NULL DEFAULT '',
			source_title TEXT NOT NULL DEFAULT '',
			quality      TEXT NOT NULL DEFAULT '',
			indexer      TEXT NOT NULL DEFAULT '',
			file_path    TEXT NOT NULL DEFAULT '',
			download_id  TEXT NOT NULL DEFAULT '',
			data_json    TEXT NOT NULL DEFAULT '{}',
			created_at   TEXT NOT NULL
		);
CREATE TABLE series_titles (
			id TEXT PRIMARY KEY,
			series_id TEXT NOT NULL,
			title TEXT NOT NULL,
			clean_title TEXT NOT NULL,
			source TEXT NOT NULL,
			UNIQUE(series_id, clean_title),
			FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE
		);
CREATE INDEX idx_series_name ON series(name)
	;
CREATE INDEX idx_episodes_series ON episodes(series_id)
	;
CREATE INDEX idx_episodes_season ON episodes(season_id)
	;
CREATE INDEX idx_seasons_series ON seasons(series_id)
	;
CREATE INDEX idx_episode_files_ep ON episode_files(episode_id)
	;
CREATE INDEX idx_history_created ON history(created_at DESC)
	;
CREATE INDEX idx_history_item ON history(item_id, created_at DESC)
	;
CREATE INDEX idx_series_titles_clean ON series_titles(clean_title)
	;
