package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	metadatav1 "github.com/Muxcore-Media/metadata-tmdb/proto/metadatav1"
	_ "modernc.org/sqlite"
)

type Module struct {
	mediaadminv1.UnimplementedMediaAdminServiceServer
	tvmgmtv1.UnimplementedTvManagementServiceServer

	mu sync.RWMutex
	db *sql.DB
	mc *client.Client

	id       string
	dbPath   string
	grpcAddr string
	httpAddr string
	imageDir string
	grpcSrv  *grpc.Server
	httpSrv  *http.Server
	grpcLis  net.Listener
	httpLis  net.Listener
}

type Config struct {
	ID       string
	DBPath   string
	GRPCAddr string
	HTTPAddr string
	ImageDir string
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "media-tvshows"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "/var/lib/media-tvshows/tvshows.db"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9440"
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":9450"
	}
	if cfg.ImageDir == "" {
		cfg.ImageDir = "/var/lib/media-tvshows/images"
	}
	if v := os.Getenv("TVSHOWS_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("TVSHOWS_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("TVSHOWS_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("TVSHOWS_IMAGE_DIR"); v != "" {
		cfg.ImageDir = v
	}
	return &Module{
		id:       cfg.ID,
		dbPath:   cfg.DBPath,
		grpcAddr: cfg.GRPCAddr,
		httpAddr: cfg.HTTPAddr,
		imageDir: cfg.ImageDir,
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:           m.id,
		Name:         "Media TV Shows",
		Version:      "0.1.0",
		Roles:        []string{"media_manager"},
		Description:  "TV show library manager with TMDB metadata import and admin UI integration",
		Author:       "MuxCore",
		Capabilities: []string{"media.library"},
		Contracts: []contracts.ContractDeclaration{
			{
				Repo:      "github.com/Muxcore-Media/contracts-media-admin",
				Interface: "MediaAdminService",
				Version:   "v0.1.0",
			},
		},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	dir := filepath.Dir(m.dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}
	if err := os.MkdirAll(m.imageDir, 0700); err != nil {
		return fmt.Errorf("create image directory: %w", err)
	}

	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return fmt.Errorf("enable WAL: %w", err)
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
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create series table: %w", err)
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
		db.Close()
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
		db.Close()
		return fmt.Errorf("create episodes table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_series_name ON series(name)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episodes_series ON episodes(series_id)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create episodes series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episodes_season ON episodes(season_id)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create episodes season index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_seasons_series ON seasons(series_id)
	`); err != nil {
		db.Close()
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
			created_at TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create episode_files table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episode_files_ep ON episode_files(episode_id)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create episode files index: %w", err)
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	grpcLis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		db.Close()
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = grpcLis

	httpLis, err := net.Listen("tcp", m.httpAddr)
	if err != nil {
		db.Close()
		return fmt.Errorf("listen HTTP %s: %w", m.httpAddr, err)
	}
	m.httpLis = httpLis

	slog.Info("media-tvshows initialized",
		"db", m.dbPath,
		"grpc", m.grpcAddr,
		"http", m.httpAddr,
	)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	mediaadminv1.RegisterMediaAdminServiceServer(m.grpcSrv, m)
	tvmgmtv1.RegisterTvManagementServiceServer(m.grpcSrv, m)

	mux := http.NewServeMux()
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(m.imageDir))))
	m.httpSrv = &http.Server{Handler: mux}

	go func() {
		slog.Info("media-tvshows gRPC service started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.grpcLis); err != nil {
			slog.Error("media-tvshows gRPC serve error", "error", err)
		}
	}()
	go func() {
		slog.Info("media-tvshows HTTP service started", "addr", m.httpAddr)
		if err := m.httpSrv.Serve(m.httpLis); err != nil && err != http.ErrServerClosed {
			slog.Error("media-tvshows HTTP serve error", "error", err)
		}
	}()

	go m.dialCore(context.Background())
	go m.subscribeToFileImported()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.httpSrv != nil {
		m.httpSrv.Shutdown(ctx)
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.mc != nil {
		m.mc.Close()
	}
	m.mu.Lock()
	if m.db != nil {
		m.db.Close()
		m.db = nil
	}
	m.mu.Unlock()
	slog.Info("media-tvshows stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("not initialized")
	}
	return db.PingContext(ctx)
}

func (m *Module) dialCore(ctx context.Context) {
	meshAddr := os.Getenv("MUXCORE_GRPC_ADDR")
	if meshAddr == "" {
		meshAddr = "localhost:9090"
	}
	insecureMode := os.Getenv("MUXCORE_GRPC_INSECURE") == "true"

	var opts []client.Option
	if insecureMode {
		opts = append(opts, client.WithInsecure())
	}

	c, err := client.Dial(meshAddr, opts...)
	if err != nil {
		slog.Error("media-tvshows: dial core", "error", err)
		return
	}
	m.mc = c
	slog.Info("media-tvshows: connected to core mesh", "addr", meshAddr)
}

func (m *Module) publish(ctx context.Context, eventType string, payload map[string]interface{}) {
	if m.mc == nil {
		return
	}
	data, _ := json.Marshal(payload)
	if err := m.mc.Events.Publish(ctx, eventType, m.id, data); err != nil {
		slog.Warn("publish event failed", "type", eventType, "error", err)
	}
}

func (m *Module) subscribeToFileImported() {
	time.Sleep(15 * time.Second)
	if m.mc == nil {
		return
	}
	ch, cancel, err := m.mc.Events.Subscribe(context.Background(), contracts.EventFileImported)
	if err != nil {
		slog.Warn("subscribe to file imported events", "error", err)
		return
	}
	go func() {
		for evt := range ch {
			var p contracts.FileImportedPayload
			if err := json.Unmarshal(evt.Payload, &p); err != nil || p.MediaType != "tv" {
				continue
			}
			m.mu.RLock()
			var seriesID string
			m.db.QueryRow(
				`SELECT id FROM series WHERE name = ? AND (year = ? OR ? = 0) LIMIT 1`,
				p.Title, p.Year, p.Year,
			).Scan(&seriesID)
			m.mu.RUnlock()
			if seriesID == "" {
				slog.Debug("no matching series for imported file", "title", p.Title)
				continue
			}
			m.mu.RLock()
			var episodeID string
			m.db.QueryRow(
				`SELECT id FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
				seriesID, p.SeasonNumber, p.EpisodeNumber,
			).Scan(&episodeID)
			m.mu.RUnlock()
			if episodeID == "" {
				slog.Debug("no matching episode for imported file", "title", p.Title, "s", p.SeasonNumber, "e", p.EpisodeNumber)
				continue
			}
			qualityStr := p.Quality
			if qualityStr == "" {
				qualityStr = "Unknown"
			}
			ext := filepath.Ext(p.DestinationPath)
			container := "mkv"
			if ext != "" {
				container = ext[1:]
			}
			m.AddEpisodeFile(context.Background(), &tvmgmtv1.AddEpisodeFileRequest{
				EpisodeId: episodeID,
				FilePath:  p.DestinationPath,
				Quality:   qualityStr,
				SizeBytes: 0,
				Container: container,
			})
		}
		cancel()
	}()
	slog.Info("subscribed to file imported events")
}

func (m *Module) findMetadataModule(ctx context.Context) (string, error) {
	if m.mc == nil {
		return "", fmt.Errorf("not connected to core")
	}
	modules, err := m.mc.Discovery.FindByCapability(ctx, "metadata")
	if err != nil {
		return "", fmt.Errorf("discover metadata: %w", err)
	}
	for _, mod := range modules {
		if mod.HttpAddr != "" {
			return mod.HttpAddr, nil
		}
	}
	return "", fmt.Errorf("no metadata module found")
}

// ── TvManagementService ────────────────────────────────────────

func (m *Module) AddTVShow(ctx context.Context, req *tvmgmtv1.AddTVShowRequest) (*tvmgmtv1.AddTVShowResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("tv_%d_%s", req.GetTmdbId(), now)

	genresJSON, _ := json.Marshal(req.GetGenres())

	_, err := m.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO series (id, tmdb_id, name, year, overview, poster_path, backdrop_path, genres, monitored, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, req.GetTmdbId(), req.GetName(), req.GetYear(),
		req.GetOverview(), req.GetPosterPath(), req.GetBackdropPath(),
		string(genresJSON), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert series: %w", err)
	}

	go m.publish(context.Background(), contracts.EventTVAdded, map[string]interface{}{
		"series_id": id, "tmdb_id": req.GetTmdbId(), "name": req.GetName(),
	})

	go m.populateSeasonsFromMetadata(context.Background(), id, req.GetTmdbId())

	return &tvmgmtv1.AddTVShowResponse{SeriesId: id}, nil
}

func (m *Module) RemoveTVShow(ctx context.Context, req *tvmgmtv1.RemoveTVShowRequest) (*tvmgmtv1.RemoveTVShowResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	_, err := m.db.ExecContext(ctx, `DELETE FROM episodes WHERE series_id = ?`, req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete episodes: %w", err)
	}
	_, err = m.db.ExecContext(ctx, `DELETE FROM seasons WHERE series_id = ?`, req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete seasons: %w", err)
	}
	_, err = m.db.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete series: %w", err)
	}

	go m.publish(context.Background(), contracts.EventTVRemoved, map[string]interface{}{
		"series_id": req.GetSeriesId(),
	})

	return &tvmgmtv1.RemoveTVShowResponse{}, nil
}

func (m *Module) RefreshMetadata(ctx context.Context, req *tvmgmtv1.RefreshMetadataRequest) (*tvmgmtv1.RefreshMetadataResponse, error) {
	m.mu.RLock()
	var tmdbID int32
	var seriesName string
	m.db.QueryRowContext(ctx, `SELECT tmdb_id, name FROM series WHERE id = ?`, req.GetSeriesId()).Scan(&tmdbID, &seriesName)
	m.mu.RUnlock()

	if tmdbID == 0 {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}

	metaAddr, err := m.findMetadataModule(ctx)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(metaAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial metadata: %w", err)
	}
	defer conn.Close()

	metaClient := metadatav1.NewMetadataServiceClient(conn)
	details, err := metaClient.GetTVDetails(ctx, &metadatav1.GetTVDetailsRequest{
		TmdbId: tmdbID,
	})
	if err != nil {
		return nil, fmt.Errorf("metadata fetch: %w", err)
	}

	genresJSON, _ := json.Marshal(details.GetGenres())
	now := time.Now().UTC().Format(time.RFC3339)

	m.mu.Lock()
	_, err = m.db.ExecContext(ctx,
		`UPDATE series SET name=?, original_name=?, year=?, overview=?, tagline=?, status=?, first_air_date=?, last_air_date=?, vote_average=?, genres=?, poster_path=?, backdrop_path=?, total_seasons=?, total_episodes=?, updated_at=? WHERE id=?`,
		details.GetName(), details.GetOriginalName(), extractYear(details.GetFirstAirDate()),
		details.GetOverview(), details.GetTagline(), details.GetStatus(),
		details.GetFirstAirDate(), details.GetLastAirDate(),
		details.GetVoteAverage(), string(genresJSON),
		details.GetPosterPath(), details.GetBackdropPath(),
		details.GetNumberOfSeasons(), details.GetNumberOfEpisodes(),
		now, req.GetSeriesId(),
	)
	m.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("update series: %w", err)
	}

	m.populateSeasonsFromDB(ctx, req.GetSeriesId(), details)

	go m.publish(context.Background(), contracts.EventTVUpdated, map[string]interface{}{
		"series_id": req.GetSeriesId(), "tmdb_id": tmdbID, "name": details.GetName(),
	})

	slog.Info("metadata refreshed", "series_id", req.GetSeriesId(), "name", details.GetName())
	return &tvmgmtv1.RefreshMetadataResponse{}, nil
}

func (m *Module) populateSeasonsFromDB(ctx context.Context, seriesID string, details *metadatav1.GetTVDetailsResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range details.GetSeasons() {
		if s.GetSeasonNumber() < 0 {
			continue
		}
		seasonID := fmt.Sprintf("sea_%s_%d", seriesID, s.GetSeasonNumber())
		_, err := m.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO seasons (id, series_id, season_number, name, overview, episode_count, air_date, poster_path, monitored, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET name=excluded.name, overview=excluded.overview, episode_count=excluded.episode_count, air_date=excluded.air_date, poster_path=excluded.poster_path, updated_at=excluded.updated_at`,
			seasonID, seriesID, s.GetSeasonNumber(), s.GetName(), s.GetOverview(),
			s.GetEpisodeCount(), s.GetAirDate(), s.GetPosterPath(), now, now,
		)
		if err != nil {
			slog.Warn("upsert season", "error", err)
		}
	}
}

func extractYear(dateStr string) int32 {
	if len(dateStr) >= 4 {
		if y, err := strconv.Atoi(dateStr[:4]); err == nil {
			return int32(y)
		}
	}
	return 0
}

func (m *Module) populateSeasonsFromMetadata(ctx context.Context, seriesID string, tmdbID int32) {
	metaAddr, err := m.findMetadataModule(ctx)
	if err != nil {
		slog.Debug("no metadata module for season population", "series", seriesID)
		return
	}

	conn, err := grpc.NewClient(metaAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Debug("dial metadata for season population", "error", err)
		return
	}
	defer conn.Close()

	metaClient := metadatav1.NewMetadataServiceClient(conn)
	details, err := metaClient.GetTVDetails(ctx, &metadatav1.GetTVDetailsRequest{
		TmdbId: tmdbID,
	})
	if err != nil {
		slog.Debug("fetch tv details for season population", "error", err)
		return
	}

	m.populateSeasonsFromDB(ctx, seriesID, details)
}

func (m *Module) ListTVShows(ctx context.Context, req *tvmgmtv1.ListTVShowsRequest) (*tvmgmtv1.ListTVShowsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	query := `SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		status, network, first_air_date, last_air_date, vote_average, genres,
		poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		created_at, updated_at FROM series`
	countQuery := `SELECT COUNT(*) FROM series`

	var args []any
	var where []string

	if req.GetSearch() != "" {
		where = append(where, `name LIKE ?`)
		args = append(args, "%"+req.GetSearch()+"%")
	}
	if req.GetGenre() != "" {
		where = append(where, `genres LIKE ?`)
		args = append(args, `%"`+req.GetGenre()+`"%`)
	}
	if len(where) > 0 {
		clause := ` WHERE ` + strings.Join(where, ` AND `)
		query += clause
		countQuery += clause
	}

	var total int
	m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)

	sortBy := req.GetSortBy()
	if sortBy == "" {
		sortBy = "name"
	}
	validSortColumns := map[string]bool{
		"name": true, "year": true, "rating": true, "status": true,
		"network": true, "added_at": true, "updated_at": true, "sort_name": true,
	}
	if !validSortColumns[sortBy] {
		sortBy = "name"
	}
	sortOrder := req.GetSortOrder()
	if sortOrder != "desc" {
		sortOrder = "asc"
	}
	query += fmt.Sprintf(` ORDER BY %s %s LIMIT ? OFFSET ?`, sortBy, sortOrder)
	queryArgs := append(args, pageSize, offset)

	rows, err := m.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("query series: %w", err)
	}
	defer rows.Close()

	var seriesList []*tvmgmtv1.TVSeries
	for rows.Next() {
		s := m.scanSeries(rows)
		if s != nil {
			seriesList = append(seriesList, s)
		}
	}

	return &tvmgmtv1.ListTVShowsResponse{
		Series:   seriesList,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

func (m *Module) GetTVShow(ctx context.Context, req *tvmgmtv1.GetTVShowRequest) (*tvmgmtv1.GetTVShowResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 created_at, updated_at FROM series WHERE id = ?`,
		req.GetSeriesId(),
	)

	series := m.scanSingleSeries(row)
	if series == nil {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}

	seasons, err := m.loadSeasons(ctx, series.GetId())
	if err != nil {
		return nil, fmt.Errorf("load seasons: %w", err)
	}
	series.Seasons = seasons

	return &tvmgmtv1.GetTVShowResponse{Series: series}, nil
}

func (m *Module) UpdateEpisodeMonitored(ctx context.Context, req *tvmgmtv1.UpdateEpisodeMonitoredRequest) (*tvmgmtv1.UpdateEpisodeMonitoredResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	monitored := 0
	if req.GetMonitored() {
		monitored = 1
	}
	_, err := m.db.ExecContext(ctx,
		`UPDATE episodes SET monitored = ?, updated_at = ? WHERE id = ?`,
		monitored, time.Now().UTC().Format(time.RFC3339), req.GetEpisodeId(),
	)
	if err != nil {
		return nil, fmt.Errorf("update episode monitored: %w", err)
	}
	return &tvmgmtv1.UpdateEpisodeMonitoredResponse{}, nil
}

func (m *Module) UpdateSeasonMonitored(ctx context.Context, req *tvmgmtv1.UpdateSeasonMonitoredRequest) (*tvmgmtv1.UpdateSeasonMonitoredResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	monitored := 0
	if req.GetMonitored() {
		monitored = 1
	}

	_, err := m.db.ExecContext(ctx,
		`UPDATE seasons SET monitored = ?, updated_at = ? WHERE id = ?`,
		monitored, now, req.GetSeasonId(),
	)
	if err != nil {
		return nil, fmt.Errorf("update season monitored: %w", err)
	}

	_, err = m.db.ExecContext(ctx,
		`UPDATE episodes SET monitored = ?, updated_at = ? WHERE season_id = ?`,
		monitored, now, req.GetSeasonId(),
	)
	if err != nil {
		return nil, fmt.Errorf("update episode monitored by season: %w", err)
	}

	return &tvmgmtv1.UpdateSeasonMonitoredResponse{}, nil
}

// ── File Management ────────────────────────────────────────────

func (m *Module) AddEpisodeFile(ctx context.Context, req *tvmgmtv1.AddEpisodeFileRequest) (*tvmgmtv1.AddEpisodeFileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("ef_%d", time.Now().UnixNano())

	_, err := m.db.ExecContext(ctx,
		`INSERT INTO episode_files (id, episode_id, file_path, quality, size_bytes, container, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, req.GetEpisodeId(), req.GetFilePath(), req.GetQuality(), req.GetSizeBytes(), req.GetContainer(), now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert episode file: %w", err)
	}
	m.db.ExecContext(ctx, `UPDATE episodes SET has_file = 1, updated_at = ? WHERE id = ?`, now, req.GetEpisodeId())

	go m.publish(context.Background(), contracts.EventTVEpisodeFileAdded, map[string]interface{}{
		"file_id": id, "episode_id": req.GetEpisodeId(), "file_path": req.GetFilePath(),
	})

	return &tvmgmtv1.AddEpisodeFileResponse{FileId: id}, nil
}

func (m *Module) RemoveEpisodeFile(ctx context.Context, req *tvmgmtv1.RemoveEpisodeFileRequest) (*tvmgmtv1.RemoveEpisodeFileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	var episodeID string
	m.db.QueryRowContext(ctx, `SELECT episode_id FROM episode_files WHERE id = ?`, req.GetFileId()).Scan(&episodeID)
	_, err := m.db.ExecContext(ctx, `DELETE FROM episode_files WHERE id = ?`, req.GetFileId())
	if err != nil {
		return nil, fmt.Errorf("delete file: %w", err)
	}

	if episodeID != "" {
		var count int
		m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM episode_files WHERE episode_id = ?`, episodeID).Scan(&count)
		if count == 0 {
			m.db.ExecContext(ctx, `UPDATE episodes SET has_file = 0, updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), episodeID)
		}
	}

	return &tvmgmtv1.RemoveEpisodeFileResponse{}, nil
}

// ── Season/Episode loading helpers ─────────────────────────────

func (m *Module) loadSeasons(ctx context.Context, seriesID string) ([]*tvmgmtv1.TVSeason, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, series_id, season_number, name, overview, episode_count,
		 air_date, poster_path, monitored, created_at, updated_at
		 FROM seasons WHERE series_id = ? ORDER BY season_number`,
		seriesID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var seasons []*tvmgmtv1.TVSeason
	for rows.Next() {
		s := m.scanSeason(rows)
		if s != nil {
			episodes, err := m.loadEpisodes(ctx, s.GetId())
			if err != nil {
				return nil, fmt.Errorf("load episodes for season %s: %w", s.GetId(), err)
			}
			s.Episodes = episodes
			seasons = append(seasons, s)
		}
	}
	return seasons, nil
}

func (m *Module) loadEpisodes(ctx context.Context, seasonID string) ([]*tvmgmtv1.TVEpisode, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, series_id, season_id, tmdb_id, episode_number, season_number,
		 absolute_number, name, overview, air_date, still_path, monitored, has_file,
		 created_at, updated_at
		 FROM episodes WHERE season_id = ? ORDER BY episode_number`,
		seasonID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var episodes []*tvmgmtv1.TVEpisode
	for rows.Next() {
		ep := m.scanEpisode(rows)
		if ep != nil {
			episodes = append(episodes, ep)
		}
	}
	return episodes, nil
}

// ── Scan helpers ───────────────────────────────────────────────

func (m *Module) scanSeries(rows *sql.Rows) *tvmgmtv1.TVSeries {
	var id, name, originalName, overview, tagline, status, network, firstAir, lastAir, genresStr, posterPath, backdropPath, createdAt, updatedAt string
	var tmdbID, year, totalSeasons, totalEpisodes int64
	var voteAvg float64
	var monitored int

	err := rows.Scan(&id, &tmdbID, &name, &originalName, &year, &overview, &tagline,
		&status, &network, &firstAir, &lastAir, &voteAvg, &genresStr,
		&posterPath, &backdropPath, &monitored, &totalSeasons, &totalEpisodes,
		&createdAt, &updatedAt)
	if err != nil {
		slog.Error("scan series row", "error", err)
		return nil
	}

	var genres []string
	json.Unmarshal([]byte(genresStr), &genres)
	if genres == nil {
		genres = []string{}
	}

	yearVal := int32(year)
	if yearVal == 0 && firstAir != "" {
		if y, err := strconv.Atoi(firstAir[:4]); err == nil {
			yearVal = int32(y)
		}
	}

	return &tvmgmtv1.TVSeries{
		Id: id, TmdbId: int32(tmdbID),
		Name: name, OriginalName: originalName,
		Year: yearVal, Overview: overview, Tagline: tagline,
		Status: status, Network: network,
		FirstAirDate: firstAir, LastAirDate: lastAir,
		VoteAverage: voteAvg, Genres: genres,
		PosterPath: posterPath, BackdropPath: backdropPath,
		Monitored:    monitored != 0,
		TotalSeasons: int32(totalSeasons), TotalEpisodes: int32(totalEpisodes),
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func (m *Module) scanSingleSeries(row *sql.Row) *tvmgmtv1.TVSeries {
	var id, name, originalName, overview, tagline, status, network, firstAir, lastAir, genresStr, posterPath, backdropPath, createdAt, updatedAt string
	var tmdbID, year, totalSeasons, totalEpisodes int64
	var voteAvg float64
	var monitored int

	err := row.Scan(&id, &tmdbID, &name, &originalName, &year, &overview, &tagline,
		&status, &network, &firstAir, &lastAir, &voteAvg, &genresStr,
		&posterPath, &backdropPath, &monitored, &totalSeasons, &totalEpisodes,
		&createdAt, &updatedAt)
	if err != nil {
		return nil
	}

	var genres []string
	json.Unmarshal([]byte(genresStr), &genres)
	if genres == nil {
		genres = []string{}
	}

	yearVal := int32(year)
	if yearVal == 0 && firstAir != "" {
		if y, err := strconv.Atoi(firstAir[:4]); err == nil {
			yearVal = int32(y)
		}
	}

	return &tvmgmtv1.TVSeries{
		Id: id, TmdbId: int32(tmdbID),
		Name: name, OriginalName: originalName,
		Year: yearVal, Overview: overview, Tagline: tagline,
		Status: status, Network: network,
		FirstAirDate: firstAir, LastAirDate: lastAir,
		VoteAverage: voteAvg, Genres: genres,
		PosterPath: posterPath, BackdropPath: backdropPath,
		Monitored:    monitored != 0,
		TotalSeasons: int32(totalSeasons), TotalEpisodes: int32(totalEpisodes),
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func (m *Module) scanSeason(rows *sql.Rows) *tvmgmtv1.TVSeason {
	var id, seriesID, name, overview, airDate, posterPath, createdAt, updatedAt string
	var seasonNumber, episodeCount int64
	var monitored int

	err := rows.Scan(&id, &seriesID, &seasonNumber, &name, &overview, &episodeCount,
		&airDate, &posterPath, &monitored, &createdAt, &updatedAt)
	if err != nil {
		slog.Error("scan season row", "error", err)
		return nil
	}

	return &tvmgmtv1.TVSeason{
		Id: id, SeriesId: seriesID,
		SeasonNumber: int32(seasonNumber), Name: name,
		Overview: overview, EpisodeCount: int32(episodeCount),
		AirDate: airDate, PosterPath: posterPath,
		Monitored: monitored != 0,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func (m *Module) scanEpisode(rows *sql.Rows) *tvmgmtv1.TVEpisode {
	var id, seriesID, seasonID, name, overview, airDate, stillPath, createdAt, updatedAt string
	var tmdbID, epNumber, seasonNumber, absNumber int64
	var monitored, hasFile int

	err := rows.Scan(&id, &seriesID, &seasonID, &tmdbID, &epNumber, &seasonNumber,
		&absNumber, &name, &overview, &airDate, &stillPath,
		&monitored, &hasFile, &createdAt, &updatedAt)
	if err != nil {
		slog.Error("scan episode row", "error", err)
		return nil
	}

	return &tvmgmtv1.TVEpisode{
		Id: id, SeriesId: seriesID, SeasonId: seasonID,
		TmdbId:        int32(tmdbID),
		EpisodeNumber: int32(epNumber), SeasonNumber: int32(seasonNumber),
		AbsoluteNumber: int32(absNumber),
		Name:           name, Overview: overview,
		AirDate: airDate, StillPath: stillPath,
		Monitored: monitored != 0, HasFile: hasFile != 0,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

// ── MediaAdminService (admin-ui contract) ─────────────────────

func (m *Module) GetMediaTypeInfo(ctx context.Context, req *mediaadminv1.GetMediaTypeInfoRequest) (*mediaadminv1.GetMediaTypeInfoResponse, error) {
	return &mediaadminv1.GetMediaTypeInfoResponse{
		DisplayName: "TV Shows",
		Icon:        "📺",
		FilterFields: []*mediaadminv1.FilterField{
			{Key: "genre", Label: "Genre", Type: "text"},
			{Key: "year", Label: "Year", Type: "number"},
			{Key: "status", Label: "Status", Type: "text"},
			{Key: "network", Label: "Network", Type: "text"},
			{Key: "has_file", Label: "Has File", Type: "select", Options: []string{"true", "false"}},
		},
	}, nil
}

func (m *Module) ListItems(ctx context.Context, req *mediaadminv1.ListItemsRequest) (*mediaadminv1.ListItemsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	query := `SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		status, network, first_air_date, last_air_date, vote_average, genres,
		poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		created_at, updated_at FROM series`
	countQuery := `SELECT COUNT(*) FROM series`

	var args []any
	var where []string

	if req.GetSearch() != "" {
		where = append(where, `name LIKE ?`)
		args = append(args, "%"+req.GetSearch()+"%")
	}

	sortBy := req.GetSortBy()
	if sortBy == "" {
		sortBy = "name"
	}
	sortOrder := req.GetSortOrder()
	if sortOrder != "desc" {
		sortOrder = "asc"
	}

	if len(where) > 0 {
		clause := ` WHERE ` + strings.Join(where, ` AND `)
		query += clause
		countQuery += clause
	}

	var total int
	m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)

	query += fmt.Sprintf(` ORDER BY %s %s LIMIT ? OFFSET ?`, sortBy, sortOrder)
	qargs := append(args, pageSize, offset)

	rows, err := m.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()

	var items []*mediaadminv1.MediaItem
	for rows.Next() {
		s := m.scanSeries(rows)
		if s != nil {
			items = append(items, seriesToMediaItem(s))
		}
	}

	return &mediaadminv1.ListItemsResponse{
		Items:    items,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

func (m *Module) GetItem(ctx context.Context, req *mediaadminv1.GetItemRequest) (*mediaadminv1.GetItemResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 created_at, updated_at FROM series WHERE id = ?`,
		req.GetId(),
	)

	series := m.scanSingleSeries(row)
	if series == nil {
		return nil, fmt.Errorf("series not found: %s", req.GetId())
	}
	return &mediaadminv1.GetItemResponse{Item: seriesToMediaItem(series)}, nil
}

func (m *Module) UpdateMetadata(ctx context.Context, req *mediaadminv1.UpdateMetadataRequest) (*mediaadminv1.UpdateMetadataResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	genresJSON, _ := json.Marshal(req.GetGenres())

	_, err := m.db.ExecContext(ctx,
		`UPDATE series SET name=?, overview=?, year=?, genres=?, updated_at=? WHERE id=?`,
		req.GetTitle(), req.GetDescription(), req.GetYear(),
		string(genresJSON), now, req.GetId(),
	)
	if err != nil {
		return nil, fmt.Errorf("update series: %w", err)
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 created_at, updated_at FROM series WHERE id = ?`,
		req.GetId(),
	)
	series := m.scanSingleSeries(row)
	if series == nil {
		return nil, fmt.Errorf("series not found after update: %s", req.GetId())
	}
	return &mediaadminv1.UpdateMetadataResponse{Item: seriesToMediaItem(series)}, nil
}

func (m *Module) ListArtwork(ctx context.Context, req *mediaadminv1.ListArtworkRequest) (*mediaadminv1.ListArtworkResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT poster_path, backdrop_path FROM series WHERE id = ?`, req.GetId(),
	)
	var poster, backdrop string
	if err := row.Scan(&poster, &backdrop); err != nil {
		return nil, fmt.Errorf("series not found: %s", req.GetId())
	}

	var artwork []*mediaadminv1.ArtworkInfo
	if poster != "" {
		artwork = append(artwork, &mediaadminv1.ArtworkInfo{
			Id:     req.GetId() + "_poster",
			ItemId: req.GetId(),
			Type:   "poster",
			Url:    fmt.Sprintf("http://%s/images/%s", m.httpAddr, poster),
		})
	}
	if backdrop != "" {
		artwork = append(artwork, &mediaadminv1.ArtworkInfo{
			Id:     req.GetId() + "_backdrop",
			ItemId: req.GetId(),
			Type:   "background",
			Url:    fmt.Sprintf("http://%s/images/%s", m.httpAddr, backdrop),
		})
	}

	return &mediaadminv1.ListArtworkResponse{Artwork: artwork}, nil
}

func (m *Module) ReplaceArtwork(stream grpc.ClientStreamingServer[mediaadminv1.ReplaceArtworkRequest, mediaadminv1.ReplaceArtworkResponse]) error {
	slog.Warn("ReplaceArtwork not yet implemented")
	return nil
}

func seriesToMediaItem(s *tvmgmtv1.TVSeries) *mediaadminv1.MediaItem {
	meta := map[string]string{
		"tmdb_id":       strconv.Itoa(int(s.GetTmdbId())),
		"status":        s.GetStatus(),
		"network":       s.GetNetwork(),
		"vote_avg":      fmt.Sprintf("%.1f", s.GetVoteAverage()),
		"season_count":  strconv.Itoa(int(s.GetTotalSeasons())),
		"episode_count": strconv.Itoa(int(s.GetTotalEpisodes())),
		"first_air":     s.GetFirstAirDate(),
		"last_air":      s.GetLastAirDate(),
	}
	if s.GetTagline() != "" {
		meta["tagline"] = s.GetTagline()
	}

	return &mediaadminv1.MediaItem{
		Id:          s.GetId(),
		Title:       s.GetName(),
		Description: s.GetOverview(),
		Year:        int64(s.GetYear()),
		Genres:      s.GetGenres(),
		Metadata:    meta,
		CreatedAt:   s.GetCreatedAt(),
		UpdatedAt:   s.GetUpdatedAt(),
	}
}

var _ contracts.Module = (*Module)(nil)
