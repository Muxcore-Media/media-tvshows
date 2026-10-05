package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	"github.com/Muxcore-Media/core/sdk/go/module/meshtls"
	rootsv1 "github.com/Muxcore-Media/media-root-folders/proto/rootsv1"
	manifest "github.com/Muxcore-Media/media-tvshows"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	_ "modernc.org/sqlite"
)

type Module struct {
	tvmgmtv1.UnimplementedTvManagementServiceServer

	mu    sync.RWMutex
	cfgMu sync.RWMutex
	db    *sql.DB
	mc    *client.Client

	id       string
	dbPath   string
	grpcAddr string
	httpAddr string
	imageDir string
	grpcSrv  *grpc.Server
	httpSrv  *http.Server
	grpcLis  net.Listener
	httpLis  net.Listener

	rootsConn   *grpc.ClientConn
	rootsClient rootsv1.RootFolderServiceClient
	rootsListFn func(ctx context.Context, mediaKind string) ([]string, error)
	// automationSearchFn overrides mesh automation SearchItem for tests.
	automationSearchFn func(ctx context.Context, req *automationv1.SearchItemRequest) (*automationv1.SearchItemResponse, error)
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
		Version:      modulesdk.ManifestVersion(manifest.ManifestJSON),
		Roles:        []string{"media_manager"},
		Description:  "TV show library manager with TMDB metadata import and admin UI integration",
		Author:       "MuxCore",
		Capabilities: []string{"media.library", "media.library.tv", "settings", "backupable"},
		Contracts: []contracts.ContractDeclaration{
			{
				Repo:      "github.com/Muxcore-Media/contracts-media-admin",
				Interface: "MediaAdminService",
				Version:   "v0.1.1",
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
	db.SetMaxOpenConns(sqliteMaxOpenConns)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return fmt.Errorf("set busy_timeout: %w", err)
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
		_ = db.Close()
		return fmt.Errorf("create series table: %w", err)
	}
	for _, col := range []string{
		`ALTER TABLE series ADD COLUMN quality_profile_id TEXT DEFAULT ''`,
		`ALTER TABLE series ADD COLUMN root_folder_path TEXT DEFAULT ''`,
		`ALTER TABLE series ADD COLUMN series_type TEXT DEFAULT 'standard'`,
	} {
		if _, err := db.ExecContext(ctx, col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
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
		_ = db.Close()
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
		_ = db.Close()
		return fmt.Errorf("create episodes table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_series_name ON series(name)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episodes_series ON episodes(series_id)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create episodes series index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episodes_season ON episodes(season_id)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create episodes season index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_seasons_series ON seasons(series_id)
	`); err != nil {
		_ = db.Close()
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
		_ = db.Close()
		return fmt.Errorf("create episode_files table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_episode_files_ep ON episode_files(episode_id)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create episode files index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS tags (
			id TEXT PRIMARY KEY,
			label TEXT UNIQUE NOT NULL,
			created_at TEXT NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create tags table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS item_tags (
			item_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			PRIMARY KEY (item_id, tag_id)
		)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create item_tags table: %w", err)
	}

	m.mu.Lock()
	m.db = db
	if err := m.ensureHistoryTable(ctx); err != nil {
		m.mu.Unlock()
		_ = db.Close()
		return err
	}
	if err := m.ensureSeriesTitlesTable(ctx); err != nil {
		m.mu.Unlock()
		_ = db.Close()
		return err
	}
	m.backfillSeriesTitles(ctx)
	m.mu.Unlock()

	grpcLis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = grpcLis

	httpLis, err := net.Listen("tcp", m.httpAddr)
	if err != nil {
		_ = db.Close()
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
	srv, err := meshtls.NewServer()
	if err != nil {
		return fmt.Errorf("gRPC mesh TLS: %w", err)
	}
	m.grpcSrv = srv
	mediaadminv1.RegisterMediaAdminServiceServer(m.grpcSrv, mediaAdminServer{m: m})
	tvmgmtv1.RegisterTvManagementServiceServer(m.grpcSrv, m)
	modulesdk.RegisterSettings(m.grpcSrv, m.id, m)

	mux := http.NewServeMux()
	mux.HandleFunc("/images/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/images/", http.FileServer(http.Dir(m.getImageDir()))).ServeHTTP(w, r)
	})
	mux.HandleFunc("/stream/tv/", m.handleStreamEpisode)
	mux.HandleFunc("GET /api/episodes/", m.handleEpisodeFileJSON)
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
	go m.subscribeToDownloadDispatched()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.httpSrv != nil {
		_ = m.httpSrv.Shutdown(ctx)
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.rootsConn != nil {
		_ = m.rootsConn.Close()
	}
	m.mu.Lock()
	if m.mc != nil {
		_ = m.mc.Close()
	}
	if m.db != nil {
		_ = m.db.Close()
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
	insecureMode := os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true"

	var opts []client.Option
	if insecureMode {
		opts = append(opts, client.WithInsecure())
	}

	c, err := client.Dial(meshAddr, opts...)
	if err != nil {
		slog.Error("media-tvshows: dial core", "error", err)
		return
	}
	m.mu.Lock()
	m.mc = c
	m.mu.Unlock()
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
			if err := m.handleFileImported(context.Background(), p); err != nil {
				slog.Debug("handle imported tv file", "title", p.Title, "error", err)
			}
		}
		cancel()
	}()
	slog.Info("subscribed to file imported events")
}

func (m *Module) handleFileImported(ctx context.Context, p contracts.FileImportedPayload) error {
	seriesID, tmdbID, err := m.resolveSeriesForImport(ctx, p)
	if err != nil {
		return err
	}
	if seriesID == "" {
		return fmt.Errorf("no matching series for %q", p.Title)
	}

	episodeIDs := m.resolveImportEpisodeIDs(ctx, seriesID, tmdbID, p)
	if len(episodeIDs) == 0 {
		return fmt.Errorf("no matching episode for %q S%dE%d abs=%d", p.Title, p.SeasonNumber, p.EpisodeNumber, p.AbsoluteNumber)
	}

	qualityStr := p.Quality
	if qualityStr == "" {
		qualityStr = "Unknown"
	}
	filePath := p.DestinationPath
	if !filepath.IsAbs(filePath) {
		// Prefer absolute library paths for local streaming; storage keys are relative.
		if filepath.IsAbs(p.StorageKey) {
			filePath = p.StorageKey
		} else if filePath == "" {
			filePath = p.StorageKey
		}
	}
	_, err = m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{
		EpisodeId:  episodeIDs[0],
		EpisodeIds: episodeIDs,
		FilePath:   filePath,
		Quality:    qualityStr,
	})
	return err
}

func normalizeSeriesType(st string) (string, error) {
	st = strings.TrimSpace(strings.ToLower(st))
	if st == "" {
		return "standard", nil
	}
	switch st {
	case "standard", "daily", "anime":
		return st, nil
	default:
		return "", fmt.Errorf("invalid series_type %q (want standard, daily, or anime)", st)
	}
}

func (m *Module) resolveImportEpisodeIDs(ctx context.Context, seriesID string, tmdbID int32, p contracts.FileImportedPayload) []string {
	seriesType := m.getSeriesType(seriesID)
	epNums := p.EpisodeNumbers
	if len(epNums) == 0 && p.EpisodeNumber > 0 {
		epNums = []int32{p.EpisodeNumber}
	}

	var ids []string
	if seriesType == "anime" && p.AbsoluteNumber > 0 {
		if id := m.findEpisodeIDByAbsolute(seriesID, p.AbsoluteNumber); id != "" {
			return []string{id}
		}
		if tmdbID != 0 {
			m.populateSeasonsFromMetadata(ctx, seriesID, tmdbID)
			if id := m.findEpisodeIDByAbsolute(seriesID, p.AbsoluteNumber); id != "" {
				return []string{id}
			}
		}
	}

	if seriesType == "daily" && p.AirDate != "" {
		if id := m.findEpisodeIDByAirDate(seriesID, p.AirDate); id != "" {
			return []string{id}
		}
		if tmdbID != 0 {
			m.populateSeasonsFromMetadata(ctx, seriesID, tmdbID)
			if id := m.findEpisodeIDByAirDate(seriesID, p.AirDate); id != "" {
				return []string{id}
			}
		}
	}

	if len(epNums) == 0 && p.AbsoluteNumber > 0 {
		if id := m.findEpisodeIDByAbsolute(seriesID, p.AbsoluteNumber); id != "" {
			return []string{id}
		}
	}

	needPopulate := false
	for _, ep := range epNums {
		if m.findEpisodeID(seriesID, p.SeasonNumber, ep) == "" {
			needPopulate = true
			break
		}
	}
	if needPopulate && tmdbID != 0 {
		m.populateSeasonsFromMetadata(ctx, seriesID, tmdbID)
	}
	for _, ep := range epNums {
		id := m.findEpisodeID(seriesID, p.SeasonNumber, ep)
		if id == "" {
			id = m.ensureStubEpisode(ctx, seriesID, p.SeasonNumber, ep)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *Module) getSeriesType(seriesID string) string {
	db := m.dbConn()
	if db == nil {
		return "standard"
	}
	var st string
	_ = db.QueryRow(`SELECT COALESCE(series_type, 'standard') FROM series WHERE id = ?`, seriesID).Scan(&st)
	if st == "" {
		return "standard"
	}
	return st
}

func (m *Module) resolveSeriesForImport(ctx context.Context, p contracts.FileImportedPayload) (seriesID string, tmdbID int32, err error) {
	title, year := splitTitleYear(p.Title, p.Year)
	if id, tid := m.findSeries(p.TMDBID, title, year); id != "" {
		return id, tid, nil
	}
	tmdbID = p.TMDBID
	name := title
	overview := ""
	poster := ""
	backdrop := ""
	var genres []string

	if tmdbID == 0 {
		result, searchErr := m.searchTVMetadata(ctx, title, year)
		if searchErr != nil {
			return "", 0, searchErr
		}
		if result == nil {
			return "", 0, nil
		}
		tmdbID = result.GetId()
		name = result.GetName()
		if name == "" {
			name = result.GetOriginalTitle()
		}
		if name == "" {
			name = result.GetTitle()
		}
		year = extractYear(result.GetFirstAirDate())
		overview = result.GetOverview()
		poster = result.GetPosterPath()
		backdrop = result.GetBackdropPath()
	}

	if id, tid := m.findSeries(tmdbID, name, year); id != "" {
		return id, tid, nil
	}

	resp, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:       tmdbID,
		Name:         name,
		Year:         year,
		Overview:     overview,
		PosterPath:   poster,
		BackdropPath: backdrop,
		Genres:       genres,
	})
	if err != nil {
		return "", 0, err
	}
	go m.syncSeriesTitlesFromTMDB(context.Background(), resp.GetSeriesId(), tmdbID, name, "")
	return resp.GetSeriesId(), tmdbID, nil
}

func (m *Module) findSeries(tmdbID int32, title string, year int32) (string, int32) {
	db := m.dbConn()
	if db == nil {
		return "", 0
	}
	var id string
	var tid int32
	if tmdbID != 0 {
		_ = db.QueryRow(`SELECT id, tmdb_id FROM series WHERE tmdb_id = ? LIMIT 1`, tmdbID).Scan(&id, &tid)
		if id != "" {
			return id, tid
		}
	}
	clean := cleanMatchTitle(title)
	if clean == "" {
		return "", 0
	}
	rows, err := db.Query(`
		SELECT s.id, s.tmdb_id, s.year FROM series s
		INNER JOIN series_titles t ON t.series_id = s.id
		WHERE t.clean_title = ?
	`, clean)
	if err != nil {
		return "", 0
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rowID string
		var rowTMDB, rowYear int32
		if err := rows.Scan(&rowID, &rowTMDB, &rowYear); err != nil {
			continue
		}
		if !yearsCompatible(year, rowYear) {
			continue
		}
		return rowID, rowTMDB
	}
	return "", 0
}

// attachListHasFile marks series that have on-disk episodes so list APIs can expose has_file
// without loading full season/episode trees (GetTVShow still returns full detail).
func (m *Module) attachListHasFile(ctx context.Context, db *sql.DB, series []*tvmgmtv1.TVSeries) {
	if db == nil || len(series) == 0 {
		return
	}
	flags := m.loadSeriesHasFile(ctx, db, series)
	for _, s := range series {
		if s == nil || !flags[s.GetId()] {
			continue
		}
		s.Seasons = []*tvmgmtv1.TVSeason{{
			Id: "_list",
			Episodes: []*tvmgmtv1.TVEpisode{{
				Id:      "_list",
				HasFile: true,
			}},
		}}
	}
}

func (m *Module) loadSeriesHasFile(ctx context.Context, db *sql.DB, series []*tvmgmtv1.TVSeries) map[string]bool {
	out := make(map[string]bool, len(series))
	if len(series) == 0 {
		return out
	}
	placeholders := make([]string, len(series))
	args := make([]any, len(series))
	for i, s := range series {
		placeholders[i] = "?"
		args[i] = s.GetId()
	}
	query := fmt.Sprintf(`
		SELECT series_id, MAX(has_file) FROM episodes
		WHERE series_id IN (%s)
		GROUP BY series_id`, strings.Join(placeholders, ","))
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		slog.Debug("load series has_file flags", "error", err)
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var has int
		if err := rows.Scan(&id, &has); err != nil {
			continue
		}
		out[id] = has > 0
	}
	return out
}

func (m *Module) findEpisodeID(seriesID string, season, episode int32) string {
	db := m.dbConn()
	if db == nil {
		return ""
	}
	var id string
	_ = db.QueryRow(
		`SELECT id FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
		seriesID, season, episode,
	).Scan(&id)
	return id
}

func (m *Module) findEpisodeIDByAbsolute(seriesID string, absolute int32) string {
	db := m.dbConn()
	if db == nil || absolute < 1 {
		return ""
	}
	var id string
	_ = db.QueryRow(
		`SELECT id FROM episodes WHERE series_id = ? AND absolute_number = ? LIMIT 1`,
		seriesID, absolute,
	).Scan(&id)
	return id
}

func (m *Module) findEpisodeIDByAirDate(seriesID, airDate string) string {
	db := m.dbConn()
	if db == nil || airDate == "" {
		return ""
	}
	var id string
	_ = db.QueryRow(
		`SELECT id FROM episodes WHERE series_id = ? AND air_date = ? LIMIT 1`,
		seriesID, airDate,
	).Scan(&id)
	return id
}

func (m *Module) ensureStubEpisode(ctx context.Context, seriesID string, season, episode int32) string {
	if season < 0 || episode < 1 {
		return ""
	}
	db := m.dbConn()
	if db == nil {
		return ""
	}
	now := time.Now().UTC().Format(time.RFC3339)
	seasonID := fmt.Sprintf("sea_%s_%d", seriesID, season)
	_, _ = db.ExecContext(ctx,
		`INSERT OR IGNORE INTO seasons (id, series_id, season_number, name, monitored, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		seasonID, seriesID, season, fmt.Sprintf("Season %d", season), now, now,
	)
	episodeID := fmt.Sprintf("ep_%s_%d_%d", seriesID, season, episode)
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO episodes (id, series_id, season_id, episode_number, season_number, name, monitored, has_file, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 1, 0, ?, ?)`,
		episodeID, seriesID, seasonID, episode, season, fmt.Sprintf("Episode %d", episode), now, now,
	)
	if err != nil {
		return ""
	}
	var id string
	_ = db.QueryRow(
		`SELECT id FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
		seriesID, season, episode,
	).Scan(&id)
	return id
}

func (m *Module) searchTVMetadata(ctx context.Context, title string, year int32) (*metadatav1.SearchResult, error) {
	metaAddr, err := m.findMetadataModule(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := meshtls.Dial(metaAddr)
	if err != nil {
		return nil, fmt.Errorf("dial metadata: %w", err)
	}
	defer func() { _ = conn.Close() }()

	metaClient := metadatav1.NewMetadataServiceClient(conn)
	resp, err := metaClient.Search(ctx, &metadatav1.SearchRequest{
		Query: title,
		Type:  metadatav1.MediaType_MEDIA_TYPE_TV,
		Year:  year,
	})
	if err != nil {
		return nil, fmt.Errorf("metadata search: %w", err)
	}
	return pickBestSearchResult(resp.GetResults(), year, false), nil
}

func pickBestSearchResult(results []*metadatav1.SearchResult, year int32, movie bool) *metadatav1.SearchResult {
	if len(results) == 0 {
		return nil
	}
	var best *metadatav1.SearchResult
	for _, r := range results {
		if movie && r.GetMediaType() != metadatav1.MediaType_MEDIA_TYPE_UNSPECIFIED &&
			r.GetMediaType() != metadatav1.MediaType_MEDIA_TYPE_MOVIE {
			continue
		}
		if !movie && r.GetMediaType() != metadatav1.MediaType_MEDIA_TYPE_UNSPECIFIED &&
			r.GetMediaType() != metadatav1.MediaType_MEDIA_TYPE_TV {
			continue
		}
		date := r.GetReleaseDate()
		if !movie {
			date = r.GetFirstAirDate()
		}
		ry := extractYear(date)
		if year != 0 && ry != 0 && ry != year {
			continue
		}
		if best == nil || r.GetPopularity() > best.GetPopularity() {
			best = r
		}
	}
	if best != nil {
		return best
	}
	if year != 0 {
		return nil
	}
	return results[0]
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
		addr := dialAddrForModule(mod.Id, mod.HttpAddr)
		if addr != "" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no metadata module found")
}

// dialAddrForModule maps discovery HttpAddr to a dial target.
// Bare/wildcard hosts use module ID (compose DNS) unless MUXCORE_MESH_DIAL_LOCAL=true.
func dialAddrForModule(moduleID, httpAddr string) string {
	if httpAddr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil || port == "" {
		return httpAddr
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return net.JoinHostPort(host, port)
	}
	if os.Getenv("MUXCORE_MESH_DIAL_LOCAL") == "true" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	if moduleID != "" {
		return net.JoinHostPort(moduleID, port)
	}
	return httpAddr
}

// ── TvManagementService ────────────────────────────────────────

func (m *Module) AddTVShow(ctx context.Context, req *tvmgmtv1.AddTVShowRequest) (*tvmgmtv1.AddTVShowResponse, error) {
	rootPath, err := m.resolveRootFolderPath(ctx, req.GetRootFolderPath(), "tv")
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.db == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("not initialized")
	}

	var existingID string
	if req.GetTmdbId() != 0 {
		_ = m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE tmdb_id = ? LIMIT 1`, req.GetTmdbId()).Scan(&existingID)
		if existingID != "" {
			m.backfillSeriesArtworkIfEmptyLocked(ctx, existingID, req.GetPosterPath(), req.GetBackdropPath())
			m.mu.Unlock()
			return &tvmgmtv1.AddTVShowResponse{SeriesId: existingID}, nil
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("tv_%d_%s", req.GetTmdbId(), now)

	genresJSON, _ := json.Marshal(req.GetGenres())

	seriesType, err := normalizeSeriesType(req.GetSeriesType())
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	_, err = m.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO series (id, tmdb_id, name, year, overview, poster_path, backdrop_path, genres, monitored, quality_profile_id, root_folder_path, series_type, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		id, req.GetTmdbId(), req.GetName(), req.GetYear(),
		req.GetOverview(), req.GetPosterPath(), req.GetBackdropPath(),
		string(genresJSON), req.GetQualityProfileId(), rootPath, seriesType, now, now,
	)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("insert series: %w", err)
	}

	_ = m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE tmdb_id = ? LIMIT 1`, req.GetTmdbId()).Scan(&existingID)
	if existingID != "" && existingID != id {
		m.mu.Unlock()
		return &tvmgmtv1.AddTVShowResponse{SeriesId: existingID}, nil
	}
	m.syncSeriesPrimaryTitlesLocked(ctx, id, req.GetName(), "")
	m.mu.Unlock()

	m.persistCachedArtwork(ctx, id, req.GetPosterPath(), req.GetBackdropPath())
	go m.syncSeriesTitlesFromTMDB(context.Background(), id, req.GetTmdbId(), req.GetName(), "")

	go m.publish(context.Background(), contracts.EventTVAdded, map[string]interface{}{
		"series_id": id, "tmdb_id": req.GetTmdbId(), "name": req.GetName(),
	})

	go m.populateSeasonsFromMetadata(context.Background(), id, req.GetTmdbId())

	return &tvmgmtv1.AddTVShowResponse{SeriesId: id}, nil
}

func (m *Module) backfillSeriesArtworkIfEmptyLocked(ctx context.Context, seriesID, posterPath, backdropPath string) {
	if m.db == nil || seriesID == "" {
		return
	}
	posterPath = strings.TrimSpace(posterPath)
	backdropPath = strings.TrimSpace(backdropPath)
	if posterPath == "" && backdropPath == "" {
		return
	}
	var currentPoster, currentBackdrop string
	_ = m.db.QueryRowContext(ctx, `SELECT poster_path, backdrop_path FROM series WHERE id = ?`, seriesID).
		Scan(&currentPoster, &currentBackdrop)
	now := time.Now().UTC().Format(time.RFC3339)
	if posterPath != "" && strings.TrimSpace(currentPoster) == "" {
		_, _ = m.db.ExecContext(ctx, `UPDATE series SET poster_path = ?, updated_at = ? WHERE id = ?`, posterPath, now, seriesID)
		currentPoster = posterPath
	}
	if backdropPath != "" && strings.TrimSpace(currentBackdrop) == "" {
		_, _ = m.db.ExecContext(ctx, `UPDATE series SET backdrop_path = ?, updated_at = ? WHERE id = ?`, backdropPath, now, seriesID)
		currentBackdrop = backdropPath
	}
	if currentPoster != "" || currentBackdrop != "" {
		go m.persistCachedArtwork(context.Background(), seriesID, currentPoster, currentBackdrop)
	}
}

func (m *Module) UpdateTVShow(ctx context.Context, req *tvmgmtv1.UpdateTVShowRequest) (*tvmgmtv1.UpdateTVShowResponse, error) {
	var rootPath *string
	if req.RootFolderPath != nil {
		resolved, err := m.resolveRootFolderPath(ctx, req.GetRootFolderPath(), "tv")
		if err != nil {
			return nil, err
		}
		rootPath = &resolved
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetSeriesId() == "" {
		return nil, fmt.Errorf("series_id required")
	}

	var sets []string
	var args []any
	if req.QualityProfileId != nil {
		sets = append(sets, `quality_profile_id = ?`)
		args = append(args, req.GetQualityProfileId())
	}
	if rootPath != nil {
		sets = append(sets, `root_folder_path = ?`)
		args = append(args, *rootPath)
	}
	if req.Monitored != nil {
		monitored := 0
		if req.GetMonitored() {
			monitored = 1
		}
		sets = append(sets, `monitored = ?`)
		args = append(args, monitored)
	}
	if req.SeriesType != nil {
		st, err := normalizeSeriesType(req.GetSeriesType())
		if err != nil {
			return nil, err
		}
		sets = append(sets, `series_type = ?`)
		args = append(args, st)
	}
	if len(sets) == 0 {
		series := m.getSeriesLocked(ctx, req.GetSeriesId())
		if series == nil {
			return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
		}
		return &tvmgmtv1.UpdateTVShowResponse{Series: series}, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sets = append(sets, `updated_at = ?`)
	args = append(args, now, req.GetSeriesId())
	res, err := m.db.ExecContext(ctx,
		`UPDATE series SET `+strings.Join(sets, `, `)+` WHERE id = ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("update series: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}

	if req.Monitored != nil {
		monitored := 0
		if req.GetMonitored() {
			monitored = 1
		}
		_, _ = m.db.ExecContext(ctx, `UPDATE seasons SET monitored = ?, updated_at = ? WHERE series_id = ?`, monitored, now, req.GetSeriesId())
		_, _ = m.db.ExecContext(ctx, `UPDATE episodes SET monitored = ?, updated_at = ? WHERE series_id = ?`, monitored, now, req.GetSeriesId())
	}

	series := m.getSeriesLocked(ctx, req.GetSeriesId())
	if series == nil {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}
	return &tvmgmtv1.UpdateTVShowResponse{Series: series}, nil
}

func (m *Module) getSeriesLocked(ctx context.Context, seriesID string) *tvmgmtv1.TVSeries {
	row := m.db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 quality_profile_id, root_folder_path, series_type, created_at, updated_at FROM series WHERE id = ?`,
		seriesID,
	)
	return m.scanSingleSeries(row)
}

func (m *Module) RemoveTVShow(ctx context.Context, req *tvmgmtv1.RemoveTVShowRequest) (*tvmgmtv1.RemoveTVShowResponse, error) {
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	var rootFolder string
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(root_folder_path, '') FROM series WHERE id = ?`, req.GetSeriesId()).Scan(&rootFolder)

	filePaths := map[string]struct{}{}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT ef.file_path FROM episode_files ef
		 INNER JOIN episodes e ON e.id = ef.episode_id
		 WHERE e.series_id = ?`, req.GetSeriesId())
	if err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" {
				filePaths[p] = struct{}{}
			}
		}
		_ = rows.Close()
	}

	if req.GetDeleteFiles() {
		for p := range filePaths {
			_ = safeDeleteMediaFile(p, rootFolder)
		}
	}

	var seriesName string
	_ = db.QueryRowContext(ctx, `SELECT name FROM series WHERE id = ?`, req.GetSeriesId()).Scan(&seriesName)
	m.appendHistory(ctx, historyEntry{
		EventType: historyDeleteItem,
		ItemID:    req.GetSeriesId(),
		Title:     seriesName,
		Data:      map[string]any{"delete_files": req.GetDeleteFiles()},
	})

	_, err = db.ExecContext(ctx,
		`DELETE FROM episode_files WHERE episode_id IN (SELECT id FROM episodes WHERE series_id = ?)`,
		req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete episode files: %w", err)
	}
	_, _ = db.ExecContext(ctx, `DELETE FROM item_tags WHERE item_id = ?`, req.GetSeriesId())
	_, _ = db.ExecContext(ctx, `DELETE FROM series_titles WHERE series_id = ?`, req.GetSeriesId())
	_, err = db.ExecContext(ctx, `DELETE FROM episodes WHERE series_id = ?`, req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete episodes: %w", err)
	}
	_, err = db.ExecContext(ctx, `DELETE FROM seasons WHERE series_id = ?`, req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete seasons: %w", err)
	}
	_, err = db.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, req.GetSeriesId())
	if err != nil {
		return nil, fmt.Errorf("delete series: %w", err)
	}

	m.removeItemArtwork(req.GetSeriesId())

	go m.publish(context.Background(), contracts.EventTVRemoved, map[string]interface{}{
		"series_id": req.GetSeriesId(),
	})

	return &tvmgmtv1.RemoveTVShowResponse{}, nil
}

func (m *Module) RefreshMetadata(ctx context.Context, req *tvmgmtv1.RefreshMetadataRequest) (*tvmgmtv1.RefreshMetadataResponse, error) {
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	var tmdbID int32
	var seriesName string
	_ = db.QueryRowContext(ctx, `SELECT tmdb_id, name FROM series WHERE id = ?`, req.GetSeriesId()).Scan(&tmdbID, &seriesName)

	if tmdbID == 0 {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}

	metaAddr, err := m.findMetadataModule(ctx)
	if err != nil {
		return nil, err
	}

	conn, err := meshtls.Dial(metaAddr)
	if err != nil {
		return nil, fmt.Errorf("dial metadata: %w", err)
	}
	defer func() { _ = conn.Close() }()

	metaClient := metadatav1.NewMetadataServiceClient(conn)
	details, err := metaClient.GetTVDetails(ctx, &metadatav1.GetTVDetailsRequest{
		Id: tmdbID,
	})
	if err != nil {
		return nil, fmt.Errorf("metadata fetch: %w", err)
	}

	genresJSON, _ := json.Marshal(details.GetGenres())
	now := time.Now().UTC().Format(time.RFC3339)

	posterSrc := details.GetPosterUrl()
	if posterSrc == "" {
		posterSrc = details.GetPosterPath()
	}
	backdropSrc := details.GetBackdropUrl()
	if backdropSrc == "" {
		backdropSrc = details.GetBackdropPath()
	}

	_, err = db.ExecContext(ctx,
		`UPDATE series SET name=?, original_name=?, year=?, overview=?, tagline=?, status=?, first_air_date=?, last_air_date=?, vote_average=?, genres=?, poster_path=?, backdrop_path=?, total_seasons=?, total_episodes=?, updated_at=? WHERE id=?`,
		details.GetName(), details.GetOriginalName(), extractYear(details.GetFirstAirDate()),
		details.GetOverview(), details.GetTagline(), details.GetStatus(),
		details.GetFirstAirDate(), details.GetLastAirDate(),
		details.GetVoteAverage(), string(genresJSON),
		details.GetPosterPath(), details.GetBackdropPath(),
		details.GetNumberOfSeasons(), details.GetNumberOfEpisodes(),
		now, req.GetSeriesId(),
	)
	if err == nil {
		m.syncSeriesPrimaryTitlesLocked(ctx, req.GetSeriesId(), details.GetName(), details.GetOriginalName())
	}
	if err != nil {
		return nil, fmt.Errorf("update series: %w", err)
	}

	m.persistCachedArtwork(ctx, req.GetSeriesId(), posterSrc, backdropSrc)
	go m.syncSeriesTitlesFromTMDB(context.Background(), req.GetSeriesId(), tmdbID, details.GetName(), details.GetOriginalName())

	m.populateSeasonsFromDB(ctx, req.GetSeriesId(), details)
	m.populateEpisodesFromMetadata(ctx, metaClient, req.GetSeriesId(), tmdbID, details)
	m.renumberAbsoluteEpisodes(ctx, req.GetSeriesId())

	go m.publish(context.Background(), contracts.EventTVUpdated, map[string]interface{}{
		"series_id": req.GetSeriesId(), "tmdb_id": tmdbID, "name": details.GetName(),
	})

	slog.Info("metadata refreshed", "series_id", req.GetSeriesId(), "name", details.GetName())
	return &tvmgmtv1.RefreshMetadataResponse{}, nil
}

func (m *Module) populateSeasonsFromDB(ctx context.Context, seriesID string, details *metadatav1.GetTVDetailsResponse) {
	db := m.dbConn()
	if db == nil {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range details.GetSeasons() {
		if s.GetSeasonNumber() < 0 {
			continue
		}
		seasonID := fmt.Sprintf("sea_%s_%d", seriesID, s.GetSeasonNumber())
		_, err := db.ExecContext(ctx,
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

func (m *Module) populateEpisodesFromMetadata(ctx context.Context, metaClient metadatav1.MetadataServiceClient, seriesID string, tmdbID int32, details *metadatav1.GetTVDetailsResponse) {
	for _, s := range details.GetSeasons() {
		if s.GetSeasonNumber() < 0 {
			continue
		}
		seasonResp, err := metaClient.GetSeasonDetails(ctx, &metadatav1.GetSeasonDetailsRequest{
			Id:           tmdbID,
			SeasonNumber: s.GetSeasonNumber(),
		})
		if err != nil {
			slog.Warn("fetch season details", "series", seriesID, "season", s.GetSeasonNumber(), "error", err)
			continue
		}
		seasonID := fmt.Sprintf("sea_%s_%d", seriesID, s.GetSeasonNumber())
		m.populateEpisodesFromSeason(ctx, seriesID, seasonID, s.GetSeasonNumber(), seasonResp.GetEpisodes())
	}
}

func (m *Module) populateEpisodesFromSeason(ctx context.Context, seriesID, seasonID string, seasonNumber int32, episodes []*metadatav1.Episode) {
	db := m.dbConn()
	if db == nil {
		return
	}

	monitored := 1
	var mon int
	if err := db.QueryRowContext(ctx, `SELECT monitored FROM seasons WHERE id = ?`, seasonID).Scan(&mon); err == nil {
		monitored = mon
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range episodes {
		if e.GetEpisodeNumber() < 1 {
			continue
		}
		sn := e.GetSeasonNumber()
		if sn == 0 {
			sn = seasonNumber
		}
		episodeID := fmt.Sprintf("ep_%s_%d_%d", seriesID, sn, e.GetEpisodeNumber())
		_, err := db.ExecContext(ctx,
			`INSERT INTO episodes (id, series_id, season_id, tmdb_id, episode_number, season_number, name, overview, air_date, still_path, monitored, has_file, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET tmdb_id=excluded.tmdb_id, name=excluded.name, overview=excluded.overview, air_date=excluded.air_date, still_path=excluded.still_path, season_id=excluded.season_id, updated_at=excluded.updated_at`,
			episodeID, seriesID, seasonID, e.GetId(), e.GetEpisodeNumber(), sn,
			e.GetName(), e.GetOverview(), e.GetAirDate(), e.GetStillPath(),
			monitored, now, now,
		)
		if err != nil {
			slog.Warn("upsert episode", "error", err, "episode", episodeID)
		}
	}
}

func (m *Module) renumberAbsoluteEpisodes(ctx context.Context, seriesID string) {
	db := m.dbConn()
	if db == nil {
		return
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM episodes WHERE series_id = ? AND season_number > 0
		 ORDER BY season_number ASC, episode_number ASC`,
		seriesID,
	)
	if err != nil {
		slog.Warn("renumber absolute query", "error", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	_ = rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for i, id := range ids {
		_, _ = db.ExecContext(ctx, `UPDATE episodes SET absolute_number = ?, updated_at = ? WHERE id = ?`, i+1, now, id)
	}
	_, _ = db.ExecContext(ctx, `UPDATE episodes SET absolute_number = 0, updated_at = ? WHERE series_id = ? AND season_number <= 0`, now, seriesID)
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

	conn, err := meshtls.Dial(metaAddr)
	if err != nil {
		slog.Debug("dial metadata for season population", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	metaClient := metadatav1.NewMetadataServiceClient(conn)
	details, err := metaClient.GetTVDetails(ctx, &metadatav1.GetTVDetailsRequest{
		Id: tmdbID,
	})
	if err != nil {
		slog.Debug("fetch tv details for season population", "error", err)
		return
	}

	m.populateSeasonsFromDB(ctx, seriesID, details)
	m.populateEpisodesFromMetadata(ctx, metaClient, seriesID, tmdbID, details)
	m.renumberAbsoluteEpisodes(ctx, seriesID)
}

func (m *Module) ListTVShows(ctx context.Context, req *tvmgmtv1.ListTVShowsRequest) (*tvmgmtv1.ListTVShowsResponse, error) {
	db := m.dbConn()
	if db == nil {
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
		quality_profile_id, root_folder_path, series_type, created_at, updated_at FROM series`
	countQuery := `SELECT COUNT(*) FROM series`

	var args []any
	var where []string

	if req.GetSearch() != "" {
		where = append(where, `(name LIKE ? OR id IN (SELECT series_id FROM series_titles WHERE title LIKE ? OR clean_title LIKE ?))`)
		q := "%" + req.GetSearch() + "%"
		args = append(args, q, q, "%"+cleanMatchTitle(req.GetSearch())+"%")
	}
	if req.GetGenre() != "" {
		where = append(where, `genres LIKE ?`)
		args = append(args, `%"`+req.GetGenre()+`"%`)
	}
	if req.GetTagId() != "" {
		where = append(where, `id IN (SELECT item_id FROM item_tags WHERE tag_id = ?)`)
		args = append(args, req.GetTagId())
	}
	if len(where) > 0 {
		clause := ` WHERE ` + strings.Join(where, ` AND `)
		query += clause
		countQuery += clause
	}

	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count series: %w", err)
	}

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

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("query series: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var seriesList []*tvmgmtv1.TVSeries
	for rows.Next() {
		s := m.scanSeries(rows)
		if s != nil {
			seriesList = append(seriesList, s)
		}
	}

	m.attachListHasFile(ctx, db, seriesList)

	return &tvmgmtv1.ListTVShowsResponse{
		Series:   seriesList,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

func (m *Module) ListMissing(ctx context.Context, req *tvmgmtv1.ListMissingRequest) (*tvmgmtv1.ListMissingResponse, error) {
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 200 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	where := `WHERE e.monitored = 1 AND e.has_file = 0 AND e.season_number > 0 AND (e.air_date = '' OR e.air_date <= date('now'))`
	var args []any
	if req.GetSeriesId() != "" {
		where += ` AND e.series_id = ?`
		args = append(args, req.GetSeriesId())
	}

	countQuery := `SELECT COUNT(*) FROM episodes e ` + where
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count missing episodes: %w", err)
	}

	query := `SELECT e.id, e.series_id, s.tmdb_id, s.name, s.year, e.season_number, e.episode_number, s.quality_profile_id,
		e.air_date, e.absolute_number, COALESCE(s.series_type, 'standard')
		FROM episodes e
		JOIN series s ON s.id = e.series_id
		` + where + `
		ORDER BY s.name ASC, e.season_number ASC, e.episode_number ASC
		LIMIT ? OFFSET ?`
	qargs := append(args, pageSize, offset)

	rows, err := db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("query missing episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*tvmgmtv1.MissingEpisodeItem
	for rows.Next() {
		var episodeID, seriesID, title, profileID, airDate, seriesType string
		var tmdbID, year, seasonNum, epNum, absNum int64
		if err := rows.Scan(&episodeID, &seriesID, &tmdbID, &title, &year, &seasonNum, &epNum, &profileID, &airDate, &absNum, &seriesType); err != nil {
			slog.Error("scan missing episode", "error", err)
			continue
		}
		items = append(items, &tvmgmtv1.MissingEpisodeItem{
			EpisodeId: episodeID, SeriesId: seriesID,
			TmdbId: int32(tmdbID), Title: title, Year: int32(year),
			SeasonNumber: int32(seasonNum), EpisodeNumber: int32(epNum),
			QualityProfileId: profileID, AirDate: airDate,
			AbsoluteNumber: int32(absNum), SeriesType: seriesType,
		})
	}

	return &tvmgmtv1.ListMissingResponse{
		Items: items, Total: int32(total),
		Page: int32(page), PageSize: int32(pageSize),
	}, nil
}

func (m *Module) GetTVShow(ctx context.Context, req *tvmgmtv1.GetTVShowRequest) (*tvmgmtv1.GetTVShowResponse, error) {
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	row := db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 quality_profile_id, root_folder_path, series_type, created_at, updated_at FROM series WHERE id = ?`,
		req.GetSeriesId(),
	)

	series := m.scanSingleSeries(row)
	if series == nil {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}

	seasons, err := m.loadSeasons(ctx, db, series.GetId())
	if err != nil {
		return nil, fmt.Errorf("load seasons: %w", err)
	}
	series.Seasons = seasons

	return &tvmgmtv1.GetTVShowResponse{Series: series}, nil
}

func (m *Module) LookupEpisode(ctx context.Context, req *tvmgmtv1.LookupEpisodeRequest) (*tvmgmtv1.LookupEpisodeResponse, error) {
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	seriesID, _ := m.findSeries(req.GetTmdbId(), req.GetTitle(), req.GetYear())
	if seriesID == "" {
		return &tvmgmtv1.LookupEpisodeResponse{Found: false}, nil
	}

	var seriesName, originalName string
	_ = db.QueryRowContext(ctx,
		`SELECT name, original_name FROM series WHERE id = ?`, seriesID,
	).Scan(&seriesName, &originalName)

	epNums := req.GetEpisodeNumbers()
	if len(epNums) == 0 && req.GetEpisodeNumber() > 0 {
		epNums = []int32{req.GetEpisodeNumber()}
	}

	type epRow struct {
		id, name, airDate string
		season, episode   int32
		absolute          int32
	}
	var episodes []epRow

	resolveOne := func(season, episode int32) (epRow, bool) {
		var r epRow
		err := db.QueryRowContext(ctx,
			`SELECT id, name, air_date, season_number, episode_number, absolute_number
			 FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
			seriesID, season, episode,
		).Scan(&r.id, &r.name, &r.airDate, &r.season, &r.episode, &r.absolute)
		if err != nil {
			return epRow{}, false
		}
		return r, true
	}

	switch {
	case len(epNums) > 0:
		season := req.GetSeasonNumber()
		for _, n := range epNums {
			if r, ok := resolveOne(season, n); ok {
				episodes = append(episodes, r)
			}
		}
	case req.GetAbsoluteNumber() > 0:
		var r epRow
		err := db.QueryRowContext(ctx,
			`SELECT id, name, air_date, season_number, episode_number, absolute_number
			 FROM episodes WHERE series_id = ? AND absolute_number = ? LIMIT 1`,
			seriesID, req.GetAbsoluteNumber(),
		).Scan(&r.id, &r.name, &r.airDate, &r.season, &r.episode, &r.absolute)
		if err == nil {
			episodes = append(episodes, r)
		}
	case req.GetAirDate() != "":
		var r epRow
		err := db.QueryRowContext(ctx,
			`SELECT id, name, air_date, season_number, episode_number, absolute_number
			 FROM episodes WHERE series_id = ? AND air_date = ? LIMIT 1`,
			seriesID, req.GetAirDate(),
		).Scan(&r.id, &r.name, &r.airDate, &r.season, &r.episode, &r.absolute)
		if err == nil {
			episodes = append(episodes, r)
		}
	}

	if len(episodes) == 0 {
		return &tvmgmtv1.LookupEpisodeResponse{
			SeriesId:     seriesID,
			SeriesName:   seriesName,
			OriginalName: originalName,
			Found:        false,
		}, nil
	}

	titles := make([]string, 0, len(episodes))
	for _, e := range episodes {
		if e.name != "" {
			titles = append(titles, e.name)
		}
	}
	first := episodes[0]
	return &tvmgmtv1.LookupEpisodeResponse{
		SeriesId:       seriesID,
		SeriesName:     seriesName,
		OriginalName:   originalName,
		EpisodeTitle:   strings.Join(titles, " + "),
		AirDate:        first.airDate,
		SeasonNumber:   first.season,
		EpisodeNumber:  first.episode,
		AbsoluteNumber: first.absolute,
		Found:          true,
	}, nil
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
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	episodeIDs := req.GetEpisodeIds()
	if len(episodeIDs) == 0 && req.GetEpisodeId() != "" {
		episodeIDs = []string{req.GetEpisodeId()}
	}
	if len(episodeIDs) == 0 {
		return nil, fmt.Errorf("episode_id required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var firstFileID string
	var seriesID, seriesName string
	for _, episodeID := range episodeIDs {
		id := fmt.Sprintf("ef_%d", time.Now().UnixNano())
		if firstFileID == "" {
			firstFileID = id
			_ = db.QueryRowContext(ctx,
				`SELECT e.series_id, s.name FROM episodes e INNER JOIN series s ON s.id = e.series_id WHERE e.id = ?`,
				episodeID,
			).Scan(&seriesID, &seriesName)
		}
		_, err := db.ExecContext(ctx,
			`INSERT INTO episode_files (id, episode_id, file_path, quality, size_bytes, container, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, episodeID, req.GetFilePath(), req.GetQuality(), req.GetSizeBytes(), req.GetContainer(), now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert episode file: %w", err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE episodes SET has_file = 1, updated_at = ? WHERE id = ?`, now, episodeID); err != nil {
			return nil, fmt.Errorf("update episode has_file: %w", err)
		}

		epID := episodeID
		fileID := id
		go m.publish(context.Background(), contracts.EventTVEpisodeFileAdded, map[string]interface{}{
			"file_id": fileID, "episode_id": epID, "file_path": req.GetFilePath(), "quality": req.GetQuality(),
		})
	}

	if seriesID != "" {
		m.appendHistory(ctx, historyEntry{
			EventType: historyImport,
			ItemID:    seriesID,
			Title:     seriesName,
			Quality:   req.GetQuality(),
			FilePath:  req.GetFilePath(),
			Data: map[string]any{
				"file_id":     firstFileID,
				"episode_ids": episodeIDs,
				"container":   req.GetContainer(),
				"size_bytes":  req.GetSizeBytes(),
			},
		})
	}

	return &tvmgmtv1.AddEpisodeFileResponse{FileId: firstFileID}, nil
}

func (m *Module) RemoveEpisodeFile(ctx context.Context, req *tvmgmtv1.RemoveEpisodeFileRequest) (*tvmgmtv1.RemoveEpisodeFileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	fileID := strings.TrimSpace(req.GetFileId())
	if fileID == "" && req.GetEpisodeId() != "" {
		_ = m.db.QueryRowContext(ctx,
			`SELECT id FROM episode_files WHERE episode_id = ? ORDER BY created_at LIMIT 1`,
			req.GetEpisodeId(),
		).Scan(&fileID)
	}
	if fileID == "" {
		return nil, fmt.Errorf("file_id or episode_id required")
	}

	var filePath string
	_ = m.db.QueryRowContext(ctx, `SELECT file_path FROM episode_files WHERE id = ?`, fileID).Scan(&filePath)
	if filePath == "" {
		_, err := m.db.ExecContext(ctx, `DELETE FROM episode_files WHERE id = ?`, fileID)
		if err != nil {
			return nil, fmt.Errorf("delete file: %w", err)
		}
		return &tvmgmtv1.RemoveEpisodeFileResponse{}, nil
	}

	rows, err := m.db.QueryContext(ctx, `SELECT DISTINCT episode_id FROM episode_files WHERE file_path = ?`, filePath)
	if err != nil {
		return nil, fmt.Errorf("query linked episodes: %w", err)
	}
	var episodeIDs []string
	for rows.Next() {
		var epID string
		if rows.Scan(&epID) == nil && epID != "" {
			episodeIDs = append(episodeIDs, epID)
		}
	}
	_ = rows.Close()

	if req.GetDeleteFiles() {
		var rootFolder string
		if len(episodeIDs) > 0 {
			_ = m.db.QueryRowContext(ctx,
				`SELECT COALESCE(s.root_folder_path, '') FROM series s
				 INNER JOIN episodes e ON e.series_id = s.id WHERE e.id = ? LIMIT 1`,
				episodeIDs[0],
			).Scan(&rootFolder)
		}
		_ = safeDeleteMediaFile(filePath, rootFolder)
	}

	var seriesID, seriesName, quality string
	if len(episodeIDs) > 0 {
		_ = m.db.QueryRowContext(ctx,
			`SELECT e.series_id, s.name FROM episodes e INNER JOIN series s ON s.id = e.series_id WHERE e.id = ? LIMIT 1`,
			episodeIDs[0],
		).Scan(&seriesID, &seriesName)
	}
	_ = m.db.QueryRowContext(ctx, `SELECT COALESCE(quality, '') FROM episode_files WHERE id = ?`, fileID).Scan(&quality)
	if seriesID != "" {
		m.appendHistory(ctx, historyEntry{
			EventType: historyDeleteFile,
			ItemID:    seriesID,
			Title:     seriesName,
			Quality:   quality,
			FilePath:  filePath,
			Data: map[string]any{
				"file_id":      fileID,
				"episode_ids":  episodeIDs,
				"delete_files": req.GetDeleteFiles(),
			},
		})
	}

	_, err = m.db.ExecContext(ctx, `DELETE FROM episode_files WHERE file_path = ?`, filePath)
	if err != nil {
		return nil, fmt.Errorf("delete file: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, episodeID := range episodeIDs {
		var count int
		if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM episode_files WHERE episode_id = ?`, episodeID).Scan(&count); err != nil {
			return nil, fmt.Errorf("count episode files: %w", err)
		}
		if count == 0 {
			if _, err := m.db.ExecContext(ctx, `UPDATE episodes SET has_file = 0, updated_at = ? WHERE id = ?`, now, episodeID); err != nil {
				return nil, fmt.Errorf("clear episode has_file: %w", err)
			}
		}
		go m.publish(context.Background(), contracts.EventTVEpisodeFileRemoved, map[string]interface{}{
			"file_id": req.GetFileId(), "episode_id": episodeID, "series_id": seriesID, "file_path": filePath,
		})
	}

	return &tvmgmtv1.RemoveEpisodeFileResponse{}, nil
}

// ── Season/Episode loading helpers ─────────────────────────────

func (m *Module) loadSeasons(ctx context.Context, db *sql.DB, seriesID string) ([]*tvmgmtv1.TVSeason, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, series_id, season_number, name, overview, episode_count,
		 air_date, poster_path, monitored, created_at, updated_at
		 FROM seasons WHERE series_id = ? ORDER BY season_number`,
		seriesID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var seasons []*tvmgmtv1.TVSeason
	byID := make(map[string]*tvmgmtv1.TVSeason)
	for rows.Next() {
		s := m.scanSeason(rows)
		if s == nil {
			continue
		}
		s.Episodes = []*tvmgmtv1.TVEpisode{}
		seasons = append(seasons, s)
		byID[s.GetId()] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	epRows, err := db.QueryContext(ctx,
		`SELECT id, series_id, season_id, tmdb_id, episode_number, season_number,
		 absolute_number, name, overview, air_date, still_path, monitored, has_file,
		 created_at, updated_at
		 FROM episodes WHERE series_id = ? ORDER BY season_number, episode_number`,
		seriesID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = epRows.Close() }()
	for epRows.Next() {
		ep := m.scanEpisode(epRows)
		if ep == nil {
			continue
		}
		if season, ok := byID[ep.GetSeasonId()]; ok {
			season.Episodes = append(season.Episodes, ep)
		}
	}
	if err := epRows.Err(); err != nil {
		return nil, err
	}
	return seasons, nil
}

// ── Scan helpers ───────────────────────────────────────────────

func (m *Module) scanSeries(rows *sql.Rows) *tvmgmtv1.TVSeries {
	var id, name, originalName, overview, tagline, status, network, firstAir, lastAir, genresStr, posterPath, backdropPath, qualityProfileID, rootFolderPath, seriesType, createdAt, updatedAt string
	var tmdbID, year, totalSeasons, totalEpisodes int64
	var voteAvg float64
	var monitored int

	err := rows.Scan(&id, &tmdbID, &name, &originalName, &year, &overview, &tagline,
		&status, &network, &firstAir, &lastAir, &voteAvg, &genresStr,
		&posterPath, &backdropPath, &monitored, &totalSeasons, &totalEpisodes,
		&qualityProfileID, &rootFolderPath, &seriesType, &createdAt, &updatedAt)
	if err != nil {
		slog.Error("scan series row", "error", err)
		return nil
	}

	var genres []string
	_ = json.Unmarshal([]byte(genresStr), &genres)
	if genres == nil {
		genres = []string{}
	}

	yearVal := int32(year)
	if yearVal == 0 && firstAir != "" {
		if y, err := strconv.Atoi(firstAir[:4]); err == nil {
			yearVal = int32(y)
		}
	}
	if seriesType == "" {
		seriesType = "standard"
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
		QualityProfileId: qualityProfileID, RootFolderPath: rootFolderPath,
		SeriesType: seriesType,
		CreatedAt:  createdAt, UpdatedAt: updatedAt,
	}
}

func (m *Module) scanSingleSeries(row *sql.Row) *tvmgmtv1.TVSeries {
	var id, name, originalName, overview, tagline, status, network, firstAir, lastAir, genresStr, posterPath, backdropPath, qualityProfileID, rootFolderPath, seriesType, createdAt, updatedAt string
	var tmdbID, year, totalSeasons, totalEpisodes int64
	var voteAvg float64
	var monitored int

	err := row.Scan(&id, &tmdbID, &name, &originalName, &year, &overview, &tagline,
		&status, &network, &firstAir, &lastAir, &voteAvg, &genresStr,
		&posterPath, &backdropPath, &monitored, &totalSeasons, &totalEpisodes,
		&qualityProfileID, &rootFolderPath, &seriesType, &createdAt, &updatedAt)
	if err != nil {
		return nil
	}

	var genres []string
	_ = json.Unmarshal([]byte(genresStr), &genres)
	if genres == nil {
		genres = []string{}
	}

	yearVal := int32(year)
	if yearVal == 0 && firstAir != "" {
		if y, err := strconv.Atoi(firstAir[:4]); err == nil {
			yearVal = int32(y)
		}
	}
	if seriesType == "" {
		seriesType = "standard"
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
		QualityProfileId: qualityProfileID, RootFolderPath: rootFolderPath,
		SeriesType: seriesType,
		CreatedAt:  createdAt, UpdatedAt: updatedAt,
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

func adminSortFieldToColumn(field mediaadminv1.SortField) string {
	switch field {
	case mediaadminv1.SortField_SORT_FIELD_YEAR:
		return "year"
	case mediaadminv1.SortField_SORT_FIELD_CREATED_AT:
		return "created_at"
	case mediaadminv1.SortField_SORT_FIELD_UPDATED_AT:
		return "updated_at"
	case mediaadminv1.SortField_SORT_FIELD_RATING:
		return "vote_average"
	case mediaadminv1.SortField_SORT_FIELD_TITLE:
		return "name"
	default:
		return "name"
	}
}

func (m *Module) GetMediaTypeInfo(ctx context.Context, req *mediaadminv1.GetMediaTypeInfoRequest) (*mediaadminv1.GetMediaTypeInfoResponse, error) {
	return &mediaadminv1.GetMediaTypeInfoResponse{
		DisplayName: "TV Shows",
		Icon:        "📺",
		FilterFields: []*mediaadminv1.FilterField{
			{Key: "genre", Label: "Genre", Type: mediaadminv1.FilterFieldType_FILTER_FIELD_TYPE_TEXT},
			{Key: "year", Label: "Year", Type: mediaadminv1.FilterFieldType_FILTER_FIELD_TYPE_NUMBER},
			{Key: "status", Label: "Status", Type: mediaadminv1.FilterFieldType_FILTER_FIELD_TYPE_TEXT},
			{Key: "network", Label: "Network", Type: mediaadminv1.FilterFieldType_FILTER_FIELD_TYPE_TEXT},
			{Key: "has_file", Label: "Has File", Type: mediaadminv1.FilterFieldType_FILTER_FIELD_TYPE_SELECT, Options: []string{"true", "false"}},
		},
		Features: []mediaadminv1.Feature{
			mediaadminv1.Feature_FEATURE_MISSING,
			mediaadminv1.Feature_FEATURE_TAGS,
			mediaadminv1.Feature_FEATURE_CALENDAR,
		},
	}, nil
}

func (m *Module) ListItems(ctx context.Context, req *mediaadminv1.ListItemsRequest) (*mediaadminv1.ListItemsResponse, error) {
	db := m.dbConn()
	if db == nil {
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
		quality_profile_id, root_folder_path, series_type, created_at, updated_at FROM series`
	countQuery := `SELECT COUNT(*) FROM series`

	var args []any
	var where []string

	if req.GetSearch() != "" {
		where = append(where, `(name LIKE ? OR id IN (SELECT series_id FROM series_titles WHERE title LIKE ? OR clean_title LIKE ?))`)
		q := "%" + req.GetSearch() + "%"
		args = append(args, q, q, "%"+cleanMatchTitle(req.GetSearch())+"%")
	}
	if req.GetTagId() != "" {
		where = append(where, `id IN (SELECT item_id FROM item_tags WHERE tag_id = ?)`)
		args = append(args, req.GetTagId())
	}

	sortBy := adminSortFieldToColumn(req.GetSortBy())
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
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count items: %w", err)
	}

	query += fmt.Sprintf(` ORDER BY %s %s LIMIT ? OFFSET ?`, sortBy, sortOrder)
	qargs := append(args, pageSize, offset)

	rows, err := db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*mediaadminv1.MediaItem
	for rows.Next() {
		s := m.scanSeries(rows)
		if s != nil {
			items = append(items, m.seriesToMediaItem(s))
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
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	row := db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 quality_profile_id, root_folder_path, series_type, created_at, updated_at FROM series WHERE id = ?`,
		req.GetId(),
	)

	series := m.scanSingleSeries(row)
	if series == nil {
		return nil, fmt.Errorf("series not found: %s", req.GetId())
	}
	return &mediaadminv1.GetItemResponse{Item: m.seriesToMediaItem(series)}, nil
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

	meta := req.GetMetadata()
	if meta != nil {
		if v, ok := meta["quality_profile_id"]; ok {
			if _, err := m.db.ExecContext(ctx, `UPDATE series SET quality_profile_id=?, updated_at=? WHERE id=?`, v, now, req.GetId()); err != nil {
				return nil, fmt.Errorf("update quality_profile_id: %w", err)
			}
		}
		if v, ok := meta["root_folder_path"]; ok {
			if _, err := m.db.ExecContext(ctx, `UPDATE series SET root_folder_path=?, updated_at=? WHERE id=?`, v, now, req.GetId()); err != nil {
				return nil, fmt.Errorf("update root_folder_path: %w", err)
			}
		}
		if v, ok := meta["series_type"]; ok {
			st, err := normalizeSeriesType(v)
			if err != nil {
				return nil, err
			}
			if _, err := m.db.ExecContext(ctx, `UPDATE series SET series_type=?, updated_at=? WHERE id=?`, st, now, req.GetId()); err != nil {
				return nil, fmt.Errorf("update series_type: %w", err)
			}
		}
		if v, ok := meta["monitored"]; ok {
			monitored := 0
			if strings.EqualFold(strings.TrimSpace(v), "true") || v == "1" || strings.EqualFold(v, "yes") {
				monitored = 1
			}
			if _, err := m.db.ExecContext(ctx, `UPDATE series SET monitored=?, updated_at=? WHERE id=?`, monitored, now, req.GetId()); err != nil {
				return nil, fmt.Errorf("update monitored: %w", err)
			}
			_, _ = m.db.ExecContext(ctx, `UPDATE seasons SET monitored = ?, updated_at = ? WHERE series_id = ?`, monitored, now, req.GetId())
			_, _ = m.db.ExecContext(ctx, `UPDATE episodes SET monitored = ?, updated_at = ? WHERE series_id = ?`, monitored, now, req.GetId())
		}
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT id, tmdb_id, name, original_name, year, overview, tagline,
		 status, network, first_air_date, last_air_date, vote_average, genres,
		 poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		 quality_profile_id, root_folder_path, series_type, created_at, updated_at FROM series WHERE id = ?`,
		req.GetId(),
	)
	series := m.scanSingleSeries(row)
	if series == nil {
		return nil, fmt.Errorf("series not found after update: %s", req.GetId())
	}
	return &mediaadminv1.UpdateMetadataResponse{Item: m.seriesToMediaItem(series)}, nil
}

func (m *Module) ListArtwork(ctx context.Context, req *mediaadminv1.ListArtworkRequest) (*mediaadminv1.ListArtworkResponse, error) {
	m.mu.RLock()
	if m.db == nil {
		m.mu.RUnlock()
		return nil, fmt.Errorf("not initialized")
	}

	var still string
	err := m.db.QueryRowContext(ctx,
		`SELECT still_path FROM episodes WHERE id = ?`, req.GetId(),
	).Scan(&still)
	if err == nil {
		m.mu.RUnlock()
		still = m.resolveEpisodeStillPath(ctx, req.GetId(), still)
		return &mediaadminv1.ListArtworkResponse{
			Artwork: m.buildEpisodeStillInfo(req.GetId(), still),
		}, nil
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT poster_path, backdrop_path FROM series WHERE id = ?`, req.GetId(),
	)
	var poster, backdrop string
	if err := row.Scan(&poster, &backdrop); err != nil {
		m.mu.RUnlock()
		return nil, fmt.Errorf("item not found: %s", req.GetId())
	}
	m.mu.RUnlock()

	poster = m.resolveServablePath(ctx, req.GetId(), "poster", poster)
	backdrop = m.resolveServablePath(ctx, req.GetId(), "backdrop", backdrop)
	return &mediaadminv1.ListArtworkResponse{
		Artwork: m.buildArtworkInfos(req.GetId(), poster, backdrop),
	}, nil
}

func (m *Module) DeleteItem(ctx context.Context, req *mediaadminv1.DeleteItemRequest) (*mediaadminv1.DeleteItemResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	m.mu.RLock()
	var exists string
	err := m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE id = ?`, req.GetId()).Scan(&exists)
	m.mu.RUnlock()
	if err != nil || exists == "" {
		return nil, status.Errorf(codes.NotFound, "series not found: %s", req.GetId())
	}

	if _, err := m.RemoveTVShow(ctx, &tvmgmtv1.RemoveTVShowRequest{
		SeriesId:    req.GetId(),
		DeleteFiles: req.GetDeleteFiles(),
	}); err != nil {
		return nil, err
	}
	return &mediaadminv1.DeleteItemResponse{}, nil
}

func (m *Module) RefreshItem(ctx context.Context, req *mediaadminv1.RefreshItemRequest) (*mediaadminv1.RefreshItemResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	if _, err := m.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: req.GetId()}); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
		return nil, err
	}
	item, err := m.GetItem(ctx, &mediaadminv1.GetItemRequest{Id: req.GetId()})
	if err != nil {
		return nil, err
	}
	return &mediaadminv1.RefreshItemResponse{Item: item.Item}, nil
}

func (m *Module) ReplaceArtwork(stream mediaadminv1.MediaAdminService_ReplaceArtworkServer) error {
	ctx := stream.Context()
	var itemID, filename string
	var artworkType mediaadminv1.ArtworkType
	var buf []byte

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch {
		case msg.GetItemId() != "":
			itemID = msg.GetItemId()
		case msg.GetArtworkType() != mediaadminv1.ArtworkType_ARTWORK_TYPE_UNSPECIFIED:
			artworkType = msg.GetArtworkType()
		case msg.GetFilename() != "":
			filename = msg.GetFilename()
		default:
			chunk := msg.GetChunk()
			if len(chunk) == 0 {
				continue
			}
			if len(buf)+len(chunk) > maxArtworkBytes {
				return status.Errorf(codes.InvalidArgument, "artwork exceeds %d bytes", maxArtworkBytes)
			}
			buf = append(buf, chunk...)
		}
	}

	if itemID == "" {
		return status.Error(codes.InvalidArgument, "item_id required")
	}
	kind, err := artworkKindFromType(artworkType)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	m.mu.RLock()
	var seriesID, episodeID string
	_ = m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE id = ?`, itemID).Scan(&seriesID)
	if seriesID == "" {
		_ = m.db.QueryRowContext(ctx, `SELECT id FROM episodes WHERE id = ?`, itemID).Scan(&episodeID)
	}
	m.mu.RUnlock()
	if seriesID == "" && episodeID == "" {
		return status.Errorf(codes.NotFound, "item not found: %s", itemID)
	}

	relPath, mime, err := m.writeArtworkBytes(itemID, kind, filename, "", buf)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "%v", err)
	}

	if episodeID != "" {
		m.mu.Lock()
		_, err = m.db.ExecContext(ctx,
			`UPDATE episodes SET still_path=?, updated_at=? WHERE id=?`,
			relPath, time.Now().UTC().Format(time.RFC3339), itemID,
		)
		m.mu.Unlock()
		if err != nil {
			return fmt.Errorf("update episode still path: %w", err)
		}
		return stream.SendAndClose(&mediaadminv1.ReplaceArtworkResponse{
			Artwork: &mediaadminv1.ArtworkInfo{
				Id: itemID + "_still", ItemId: itemID,
				Type: mediaadminv1.ArtworkType_ARTWORK_TYPE_STILL, Url: artworkURL(m.httpAddr, relPath),
				MimeType: mime,
			},
		})
	}

	col := "poster_path"
	if kind == "backdrop" {
		col = "backdrop_path"
	}
	m.mu.Lock()
	_, err = m.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE series SET %s=?, updated_at=? WHERE id=?`, col),
		relPath, time.Now().UTC().Format(time.RFC3339), itemID,
	)
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("update artwork path: %w", err)
	}

	return stream.SendAndClose(&mediaadminv1.ReplaceArtworkResponse{
		Artwork: &mediaadminv1.ArtworkInfo{
			Id: itemID + "_" + kind, ItemId: itemID,
			Type: artworkTypeForKind(kind), Url: artworkURL(m.httpAddr, relPath),
			MimeType: mime,
		},
	})
}

func (m *Module) seriesToMediaItem(s *tvmgmtv1.TVSeries) *mediaadminv1.MediaItem {
	meta := map[string]string{
		"tmdb_id":            strconv.Itoa(int(s.GetTmdbId())),
		"status":             s.GetStatus(),
		"network":            s.GetNetwork(),
		"vote_avg":           fmt.Sprintf("%.1f", s.GetVoteAverage()),
		"season_count":       strconv.Itoa(int(s.GetTotalSeasons())),
		"episode_count":      strconv.Itoa(int(s.GetTotalEpisodes())),
		"first_air":          s.GetFirstAirDate(),
		"last_air":           s.GetLastAirDate(),
		"monitored":          strconv.FormatBool(s.GetMonitored()),
		"quality_profile_id": s.GetQualityProfileId(),
		"root_folder_path":   s.GetRootFolderPath(),
		"series_type":        s.GetSeriesType(),
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
		Artwork:     m.buildArtworkInfos(s.GetId(), s.GetPosterPath(), s.GetBackdropPath()),
		CreatedAt:   s.GetCreatedAt(),
		UpdatedAt:   s.GetUpdatedAt(),
	}
}

func episodeIDFromPath(path, prefix string) string {
	id := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if decoded, err := url.PathUnescape(id); err == nil {
		id = decoded
	}
	id = strings.TrimSuffix(id, "/file")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func (m *Module) lookupEpisodeFile(ctx context.Context, episodeID string) (filePath, fileID, quality string, ok bool) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil || episodeID == "" {
		return "", "", "", false
	}
	err := db.QueryRowContext(ctx,
		`SELECT id, file_path, COALESCE(quality, '') FROM episode_files WHERE episode_id = ? ORDER BY created_at LIMIT 1`, episodeID,
	).Scan(&fileID, &filePath, &quality)
	if err != nil {
		return "", "", "", false
	}
	if filePath != "" && !filepath.IsAbs(filePath) {
		var root string
		_ = db.QueryRowContext(ctx,
			`SELECT COALESCE(s.root_folder_path, '') FROM episodes e
			 INNER JOIN series s ON s.id = e.series_id WHERE e.id = ?`, episodeID,
		).Scan(&root)
		if root != "" {
			rel := strings.TrimPrefix(filepath.ToSlash(filePath), "media/")
			candidate := filepath.Join(root, filepath.FromSlash(rel))
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				filePath = candidate
			}
		}
	}
	if filePath == "" {
		return "", "", "", false
	}
	return filePath, fileID, quality, true
}

// handleEpisodeFileJSON returns the on-disk path for an episode so mediauiprox
// can probe sidecar subtitles and chapters without a GetMedia RPC.
func (m *Module) handleEpisodeFileJSON(w http.ResponseWriter, r *http.Request) {
	id := episodeIDFromPath(r.URL.Path, "/api/episodes/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	filePath, fileID, quality, ok := m.lookupEpisodeFile(r.Context(), id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":        id,
		"file_id":   fileID,
		"file_path": filePath,
		"quality":   quality,
	})
}

// handleStreamEpisode serves an episode file for browser playback.
// Path: GET /stream/tv/{episode_id}
func (m *Module) handleStreamEpisode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := episodeIDFromPath(r.URL.Path, "/stream/tv/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	filePath, _, _, ok := m.lookupEpisodeFile(r.Context(), id)
	if !ok || !filepath.IsAbs(filePath) {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		slog.Warn("stream open failed", "episode_id", id, "path", filePath, "error", err)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filepath.Base(filePath), st.ModTime(), f)
}

var _ contracts.Module = (*Module)(nil)
