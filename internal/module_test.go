package internal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func newTestModule(t *testing.T) *Module {
	t.Helper()
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "tvshows.db"),
		ImageDir: filepath.Join(t.TempDir(), "images"),
		GRPCAddr: ":0",
		HTTPAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	return m
}

func TestModuleInfo(t *testing.T) {
	m := NewModule(Config{})
	info := m.Info()
	if info.ID == "" {
		t.Error("module ID must not be empty")
	}
	if info.Version == "" {
		t.Error("module version must not be empty")
	}
	caps := map[string]bool{}
	for _, c := range info.Capabilities {
		caps[c] = true
	}
	if !caps["media.library"] || !caps["media.library.tv"] {
		t.Errorf("expected media.library and media.library.tv, got %v", info.Capabilities)
	}
	if len(info.Roles) == 0 || info.Roles[0] != "media_manager" {
		t.Errorf("expected role media_manager, got %v", info.Roles)
	}
}

func TestAddAndGetTVShow(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:   1668,
		Name:     "Breaking Bad",
		Year:     2008,
		Overview: "A high school chemistry teacher diagnosed with inoperable lung cancer turns to manufacturing and selling methamphetamine.",
		Genres:   []string{"Drama", "Crime", "Thriller"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if add.SeriesId == "" {
		t.Fatal("expected non-empty series ID")
	}

	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if get.Series.Name != "Breaking Bad" {
		t.Errorf("expected 'Breaking Bad', got %s", get.Series.Name)
	}
	if get.Series.Year != 2008 {
		t.Errorf("expected year 2008, got %d", get.Series.Year)
	}
	if len(get.Series.Genres) != 3 {
		t.Fatalf("expected 3 genres, got %d", len(get.Series.Genres))
	}
}

func TestAddDuplicateTMDBID(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})
	_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})

	list, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Errorf("expected 1 series (unique TMDB ID), got %d", list.Total)
	}
}

func TestRemoveTVShow(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})

	_, err := m.RemoveTVShow(ctx, &tvmgmtv1.RemoveTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}

	_, err = m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err == nil {
		t.Fatal("expected error after removal")
	}
}

func TestListTVShowsPagination(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
			TmdbId: int32(100 + i),
			Name:   fmt.Sprintf("Show %d", i+1),
			Year:   2000 + int32(i),
		})
	}

	page1, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Series) != 2 {
		t.Errorf("expected 2 series on page 1, got %d", len(page1.Series))
	}
	if page1.Total != 5 {
		t.Errorf("expected total 5, got %d", page1.Total)
	}

	page3, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Series) != 1 {
		t.Errorf("expected 1 series on page 3, got %d", len(page3.Series))
	}
}

func TestSearchTVShows(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1, Name: "Breaking Bad", Year: 2008})
	_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 2, Name: "Better Call Saul", Year: 2015})
	_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 3, Name: "Inception", Year: 2010})

	resp, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{Search: "breaking"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Errorf("expected 1 show, got %d", resp.Total)
	}
}

func TestGetMediaTypeInfo(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	info, err := m.GetMediaTypeInfo(ctx, &mediaadminv1.GetMediaTypeInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.DisplayName != "TV Shows" {
		t.Errorf("expected 'TV Shows', got %s", info.DisplayName)
	}
	if len(info.FilterFields) == 0 {
		t.Error("expected filter fields")
	}
	want := map[mediaadminv1.Feature]bool{
		mediaadminv1.Feature_FEATURE_MISSING:  true,
		mediaadminv1.Feature_FEATURE_TAGS:     true,
		mediaadminv1.Feature_FEATURE_CALENDAR: true,
	}
	for _, f := range info.Features {
		delete(want, f)
	}
	if len(want) != 0 {
		t.Errorf("missing features: %v", want)
	}
}

func TestListItems(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, _ = m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1, Name: "Test Show", Year: 2020})

	resp, err := m.ListItems(ctx, &mediaadminv1.ListItemsRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if resp.Items[0].Title != "Test Show" {
		t.Errorf("expected 'Test Show', got %s", resp.Items[0].Title)
	}
	if resp.Items[0].Metadata["tmdb_id"] != "1" {
		t.Errorf("expected tmdb_id=1 in metadata, got %s", resp.Items[0].Metadata["tmdb_id"])
	}
}

func TestGetItem(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})

	resp, err := m.GetItem(ctx, &mediaadminv1.GetItemRequest{Id: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Item.Title != "Breaking Bad" {
		t.Errorf("expected 'Breaking Bad', got %s", resp.Item.Title)
	}
}

func TestUpdateMetadata(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})

	updated, err := m.UpdateMetadata(ctx, &mediaadminv1.UpdateMetadataRequest{
		Id:          add.SeriesId,
		Title:       "Breaking Bad (Updated)",
		Description: "New description",
		Year:        2008,
		Metadata: map[string]string{
			"monitored":   "false",
			"series_type": "anime",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Item.Title != "Breaking Bad (Updated)" {
		t.Errorf("expected updated title, got %s", updated.Item.Title)
	}
	if updated.Item.Metadata["monitored"] != "false" {
		t.Errorf("expected monitored=false, got %q", updated.Item.Metadata["monitored"])
	}
	if updated.Item.Metadata["series_type"] != "anime" {
		t.Errorf("expected series_type=anime, got %q", updated.Item.Metadata["series_type"])
	}
}

func TestListArtwork(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	relPoster := "test123/poster.jpg"
	relBackdrop := "test123/backdrop.jpg"
	if err := os.MkdirAll(filepath.Join(m.imageDir, "test123"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.imageDir, relPoster), []byte("poster"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.imageDir, relBackdrop), []byte("backdrop"), 0600); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, poster_path, backdrop_path, monitored, created_at, updated_at)
		 VALUES ('test123', 1, 'Test', 2020, ?, ?, 1, 'now', 'now')`, relPoster, relBackdrop)
	m.mu.Unlock()

	resp, err := m.ListArtwork(ctx, &mediaadminv1.ListArtworkRequest{Id: "test123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Artwork) != 2 {
		t.Fatalf("expected 2 artwork entries, got %d", len(resp.Artwork))
	}
	if resp.Artwork[0].Type != mediaadminv1.ArtworkType_ARTWORK_TYPE_POSTER {
		t.Errorf("expected first artwork type poster, got %v", resp.Artwork[0].Type)
	}
	if !strings.Contains(resp.Artwork[0].Url, "/images/"+relPoster) {
		t.Errorf("unexpected poster url: %s", resp.Artwork[0].Url)
	}
}

func TestUpdateEpisodeMonitored(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	mod := m
	mod.mu.Lock()
	_, _ = mod.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Test', 2020, 1, 'now', 'now')`)
	_, _ = mod.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = mod.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, tmdb_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, 1, 1, 0, 'now', 'now')`)
	mod.mu.Unlock()

	_, err := m.UpdateEpisodeMonitored(ctx, &tvmgmtv1.UpdateEpisodeMonitoredRequest{
		EpisodeId: "ep1",
		Monitored: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	var monitored int
	mod.mu.RLock()
	_ = mod.db.QueryRowContext(ctx, `SELECT monitored FROM episodes WHERE id = 'ep1'`).Scan(&monitored)
	mod.mu.RUnlock()
	if monitored != 0 {
		t.Errorf("expected monitored=0, got %d", monitored)
	}
}

func TestUpdateSeasonMonitored(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	mod := m
	mod.mu.Lock()
	_, _ = mod.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Test', 2020, 1, 'now', 'now')`)
	_, _ = mod.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = mod.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, tmdb_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, 1, 1, 0, 'now', 'now')`)
	mod.mu.Unlock()

	_, err := m.UpdateSeasonMonitored(ctx, &tvmgmtv1.UpdateSeasonMonitoredRequest{
		SeasonId:  "se1",
		Monitored: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	var monitored int
	mod.mu.RLock()
	_ = mod.db.QueryRowContext(ctx, `SELECT monitored FROM seasons WHERE id = 'se1'`).Scan(&monitored)
	mod.mu.RUnlock()
	if monitored != 0 {
		t.Errorf("expected season monitored=0, got %d", monitored)
	}

	mod.mu.RLock()
	_ = mod.db.QueryRowContext(ctx, `SELECT monitored FROM episodes WHERE id = 'ep1'`).Scan(&monitored)
	mod.mu.RUnlock()
	if monitored != 0 {
		t.Errorf("expected episode monitored=0, got %d", monitored)
	}
}

func TestHealth(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	if err := m.Health(ctx); err != nil {
		t.Fatal("expected health to pass after init")
	}
}

func TestLifecycle(t *testing.T) {
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "lifecycle.db"),
		ImageDir: filepath.Join(t.TempDir(), "images"),
		GRPCAddr: ":0",
		HTTPAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPopulateEpisodesFromSeason(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1396, 'Breaking Bad', 2008, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('sea_s1_1', 's1', 1, 1, 'now', 'now')`)
	m.mu.Unlock()

	eps := []*metadatav1.Episode{
		{Id: 62085, Name: "Pilot", Overview: "Walter White.", AirDate: "2008-01-20", EpisodeNumber: 1, SeasonNumber: 1, StillPath: "/still.jpg"},
		{Id: 62086, Name: "Cat's in the Bag...", Overview: "Cleanup.", AirDate: "2008-01-27", EpisodeNumber: 2, SeasonNumber: 1},
	}
	m.populateEpisodesFromSeason(ctx, "s1", "sea_s1_1", 1, eps)

	var id, name string
	var hasFile, monitored int
	m.mu.RLock()
	err := m.db.QueryRowContext(ctx,
		`SELECT id, name, has_file, monitored FROM episodes WHERE series_id = ? AND season_number = 1 AND episode_number = 1`,
		"s1",
	).Scan(&id, &name, &hasFile, &monitored)
	m.mu.RUnlock()
	if err != nil {
		t.Fatalf("expected S01E01 row: %v", err)
	}
	if id != "ep_s1_1_1" {
		t.Errorf("expected ep_s1_1_1, got %s", id)
	}
	if name != "Pilot" {
		t.Errorf("expected Pilot, got %s", name)
	}
	if hasFile != 0 || monitored != 1 {
		t.Errorf("expected has_file=0 monitored=1, got %d %d", hasFile, monitored)
	}

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `UPDATE episodes SET has_file = 1, monitored = 0 WHERE id = ?`, id)
	m.mu.Unlock()

	eps[0].Name = "Pilot (Updated)"
	m.populateEpisodesFromSeason(ctx, "s1", "sea_s1_1", 1, eps)

	m.mu.RLock()
	err = m.db.QueryRowContext(ctx,
		`SELECT name, has_file, monitored FROM episodes WHERE id = ?`, id,
	).Scan(&name, &hasFile, &monitored)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if name != "Pilot (Updated)" {
		t.Errorf("expected updated name, got %s", name)
	}
	if hasFile != 1 {
		t.Errorf("expected has_file preserved as 1, got %d", hasFile)
	}
	if monitored != 0 {
		t.Errorf("expected monitored preserved as 0, got %d", monitored)
	}
}

func TestPopulateEpisodesInheritsSeasonMonitored(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Test', 2020, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('sea_s1_1', 's1', 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	m.populateEpisodesFromSeason(ctx, "s1", "sea_s1_1", 1, []*metadatav1.Episode{
		{Id: 1, Name: "E1", EpisodeNumber: 1, SeasonNumber: 1},
	})

	var monitored int
	m.mu.RLock()
	_ = m.db.QueryRowContext(ctx, `SELECT monitored FROM episodes WHERE id = 'ep_s1_1_1'`).Scan(&monitored)
	m.mu.RUnlock()
	if monitored != 0 {
		t.Errorf("expected monitored=0 from season, got %d", monitored)
	}
}

func TestImportMatchSetsHasFile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1396, 'Breaking Bad', 2008, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('sea_s1_1', 's1', 1, 1, 'now', 'now')`)
	m.mu.Unlock()

	m.populateEpisodesFromSeason(ctx, "s1", "sea_s1_1", 1, []*metadatav1.Episode{
		{Id: 62085, Name: "Pilot", EpisodeNumber: 1, SeasonNumber: 1},
	})

	title, year := "Breaking Bad", 2008
	seasonNum, episodeNum := 1, 1
	dest := "/library/Breaking Bad/S01E01.mkv"

	m.mu.RLock()
	var seriesID string
	_ = m.db.QueryRow(
		`SELECT id FROM series WHERE name = ? AND (year = ? OR ? = 0) LIMIT 1`,
		title, year, year,
	).Scan(&seriesID)
	var episodeID string
	_ = m.db.QueryRow(
		`SELECT id FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
		seriesID, seasonNum, episodeNum,
	).Scan(&episodeID)
	m.mu.RUnlock()

	if seriesID == "" || episodeID == "" {
		t.Fatalf("import match failed: series=%q episode=%q", seriesID, episodeID)
	}

	_, err := m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{
		EpisodeId: episodeID,
		FilePath:  dest,
		Quality:   "1080p",
		Container: "mkv",
	})
	if err != nil {
		t.Fatal(err)
	}

	var hasFile int
	m.mu.RLock()
	_ = m.db.QueryRowContext(ctx, `SELECT has_file FROM episodes WHERE id = ?`, episodeID).Scan(&hasFile)
	m.mu.RUnlock()
	if hasFile != 1 {
		t.Errorf("expected has_file=1 after import match, got %d", hasFile)
	}
}

func TestFindSeriesByTMDBAndTitle(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 1396,
		Name:   "Breaking Bad",
		Year:   2008,
	})
	if err != nil {
		t.Fatal(err)
	}

	if id, tid := m.findSeries(1396, "", 0); id != add.SeriesId || tid != 1396 {
		t.Errorf("tmdb match: got %q/%d want %q/1396", id, tid, add.SeriesId)
	}
	if id, _ := m.findSeries(0, "breaking bad", 2008); id != add.SeriesId {
		t.Errorf("title+year match: got %q", id)
	}
}

func TestHandleFileImportedTVStubEpisode(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 1399,
		Name:   "Game of Thrones",
		Year:   2011,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = m.handleFileImported(ctx, contracts.FileImportedPayload{
		MediaType:       "tv",
		Title:           "Game of Thrones",
		Year:            2011,
		TMDBID:          1399,
		SeasonNumber:    1,
		EpisodeNumber:   1,
		StorageKey:      "media/TV/Game of Thrones/Season 01/ep.mkv",
		DestinationPath: "/data/media/TV/Game of Thrones/Season 01/ep.mkv",
		Quality:         "1080p",
	})
	if err != nil {
		t.Fatal(err)
	}

	epID := m.findEpisodeID(add.SeriesId, 1, 1)
	if epID == "" {
		t.Fatal("expected stub episode to exist")
	}
	var hasFile int
	var path string
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT has_file FROM episodes WHERE id = ?`, epID).Scan(&hasFile)
	_ = m.db.QueryRow(`SELECT file_path FROM episode_files WHERE episode_id = ?`, epID).Scan(&path)
	m.mu.RUnlock()
	if hasFile != 1 {
		t.Errorf("expected has_file=1, got %d", hasFile)
	}
	want := "/data/media/TV/Game of Thrones/Season 01/ep.mkv"
	if path != want {
		t.Errorf("unexpected path %s (want absolute destination)", path)
	}
}

func TestAddTVShowReturnsExistingID(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	first, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1412, Name: "Arrow", Year: 2012})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1412, Name: "Arrow", Year: 2012})
	if err != nil {
		t.Fatal(err)
	}
	if first.SeriesId != second.SeriesId {
		t.Errorf("expected same series id, got %s vs %s", first.SeriesId, second.SeriesId)
	}
}

func TestSeriesQualityProfileBinding(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	profileID := "qp_tv_1"
	root := "/media/tv"
	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:           1396,
		Name:             "Breaking Bad",
		Year:             2008,
		QualityProfileId: profileID,
		RootFolderPath:   root,
	})
	if err != nil {
		t.Fatal(err)
	}

	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if get.Series.QualityProfileId != profileID {
		t.Errorf("quality_profile_id: got %q want %q", get.Series.QualityProfileId, profileID)
	}
	if get.Series.RootFolderPath != root {
		t.Errorf("root_folder_path: got %q want %q", get.Series.RootFolderPath, root)
	}

	newProfile := "qp_tv_2"
	newRoot := "/media/tv-uhd"
	upd, err := m.UpdateTVShow(ctx, &tvmgmtv1.UpdateTVShowRequest{
		SeriesId:         add.SeriesId,
		QualityProfileId: &newProfile,
		RootFolderPath:   &newRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Series.QualityProfileId != newProfile || upd.Series.RootFolderPath != newRoot {
		t.Errorf("update binding: got profile=%q root=%q", upd.Series.QualityProfileId, upd.Series.RootFolderPath)
	}

	item, err := m.GetItem(ctx, &mediaadminv1.GetItemRequest{Id: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if item.Item.Metadata["quality_profile_id"] != newProfile {
		t.Errorf("admin metadata quality_profile_id: %q", item.Item.Metadata["quality_profile_id"])
	}
	if item.Item.Metadata["root_folder_path"] != newRoot {
		t.Errorf("admin metadata root_folder_path: %q", item.Item.Metadata["root_folder_path"])
	}
}

func TestDeleteItem(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})
	if err != nil {
		t.Fatal(err)
	}
	artDir := filepath.Join(m.imageDir, add.SeriesId)
	if err := os.MkdirAll(artDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "poster.jpg"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.DeleteItem(ctx, &mediaadminv1.DeleteItemRequest{Id: add.SeriesId}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId}); err == nil {
		t.Fatal("expected error after delete")
	}
	if _, err := os.Stat(artDir); !os.IsNotExist(err) {
		t.Fatalf("expected artwork dir removed, err=%v", err)
	}
}

func TestRefreshItemNotFound(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.RefreshItem(ctx, &mediaadminv1.RefreshItemRequest{Id: "missing"})
	if err == nil {
		t.Fatal("expected not found")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestCacheRemoteArtwork(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
	}))
	t.Cleanup(srv.Close)

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1, Name: "Art", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}

	rel, mime, err := m.cacheRemoteArtwork(ctx, add.SeriesId, "poster", srv.URL+"/poster.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if rel != add.SeriesId+"/poster.jpg" {
		t.Errorf("rel path: got %q", rel)
	}
	if !strings.Contains(mime, "jpeg") {
		t.Errorf("mime: %q", mime)
	}
	if !m.localArtworkExists(rel) {
		t.Fatal("expected local file")
	}

	m.persistCachedArtwork(ctx, add.SeriesId, srv.URL+"/poster.jpg", srv.URL+"/backdrop.jpg")
	var poster string
	m.mu.RLock()
	_ = m.db.QueryRowContext(ctx, `SELECT poster_path FROM series WHERE id = ?`, add.SeriesId).Scan(&poster)
	m.mu.RUnlock()
	if poster != add.SeriesId+"/poster.jpg" {
		t.Errorf("db poster_path: got %q", poster)
	}
}

func TestReplaceArtwork(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 2, Name: "Replace", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}

	stream := &fakeReplaceStream{
		ctx: ctx,
		msgs: []*mediaadminv1.ReplaceArtworkRequest{
			{Data: &mediaadminv1.ReplaceArtworkRequest_ItemId{ItemId: add.SeriesId}},
			{Data: &mediaadminv1.ReplaceArtworkRequest_ArtworkType{ArtworkType: mediaadminv1.ArtworkType_ARTWORK_TYPE_POSTER}},
			{Data: &mediaadminv1.ReplaceArtworkRequest_Filename{Filename: "custom.png"}},
			{Data: &mediaadminv1.ReplaceArtworkRequest_Chunk{Chunk: []byte{0x89, 0x50, 0x4e, 0x47}}},
		},
	}
	if err := m.ReplaceArtwork(stream); err != nil {
		t.Fatal(err)
	}
	if stream.resp == nil || stream.resp.Artwork == nil {
		t.Fatal("expected artwork response")
	}
	rel := add.SeriesId + "/poster.png"
	if !m.localArtworkExists(rel) {
		t.Fatal("expected written poster.png")
	}
	list, err := m.ListArtwork(ctx, &mediaadminv1.ListArtworkRequest{Id: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Artwork) < 1 {
		t.Fatal("expected list artwork")
	}
	if !strings.Contains(list.Artwork[0].Url, "/images/"+rel) {
		t.Errorf("url: %s", list.Artwork[0].Url)
	}
}

type fakeReplaceStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*mediaadminv1.ReplaceArtworkRequest
	idx  int
	resp *mediaadminv1.ReplaceArtworkResponse
}

func (s *fakeReplaceStream) Context() context.Context { return s.ctx }

func (s *fakeReplaceStream) Recv() (*mediaadminv1.ReplaceArtworkRequest, error) {
	if s.idx >= len(s.msgs) {
		return nil, io.EOF
	}
	msg := s.msgs[s.idx]
	s.idx++
	return msg, nil
}

func (s *fakeReplaceStream) SendAndClose(resp *mediaadminv1.ReplaceArtworkResponse) error {
	s.resp = resp
	return nil
}

func (s *fakeReplaceStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeReplaceStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeReplaceStream) SetTrailer(metadata.MD)       {}
func (s *fakeReplaceStream) SendMsg(any) error            { return nil }
func (s *fakeReplaceStream) RecvMsg(any) error            { return nil }

func TestListMissingEpisodes(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, quality_profile_id, created_at, updated_at)
		 VALUES ('s1', 1396, 'Breaking Bad', 2008, 1, 'qp_tv', 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s2', 2, 'Other', 2010, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now'), ('se2', 's2', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, tmdb_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES
		 ('ep_miss', 's1', 'se1', 1, 1, 1, 1, 0, 'now', 'now'),
		 ('ep_has', 's1', 'se1', 2, 2, 1, 1, 1, 'now', 'now'),
		 ('ep_unmon', 's1', 'se1', 3, 3, 1, 0, 0, 'now', 'now'),
		 ('ep_other', 's2', 'se2', 4, 1, 1, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	resp, err := m.ListMissing(ctx, &tvmgmtv1.ListMissingRequest{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 missing, got %d", resp.Total)
	}

	filtered, err := m.ListMissing(ctx, &tvmgmtv1.ListMissingRequest{Page: 1, PageSize: 50, SeriesId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 {
		t.Fatalf("expected 1 for series s1, got %d", filtered.Total)
	}
	item := filtered.Items[0]
	if item.EpisodeId != "ep_miss" || item.SeasonNumber != 1 || item.EpisodeNumber != 1 {
		t.Errorf("unexpected item: %+v", item)
	}
	if item.Title != "Breaking Bad" || item.QualityProfileId != "qp_tv" || item.TmdbId != 1396 {
		t.Errorf("series fields: %+v", item)
	}
}

func TestListMissingExcludesSeasonZeroSpecials(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 253, 'Star Trek', 1966, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se0', 's1', 0, 1, 'now', 'now'), ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, tmdb_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES
		 ('ep_s00e03', 's1', 'se0', 1, 3, 0, 1, 0, 'now', 'now'),
		 ('ep_s01e01', 's1', 'se1', 2, 1, 1, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	resp, err := m.ListMissing(ctx, &tvmgmtv1.ListMissingRequest{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected 1 missing (S01 only), got %d", resp.Total)
	}
	if resp.Items[0].EpisodeId != "ep_s01e01" || resp.Items[0].SeasonNumber != 1 {
		t.Fatalf("got %+v", resp.Items[0])
	}
}

func TestListMissingExcludesUnaired(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, series_type, created_at, updated_at)
		 VALUES ('s1', 1, 'Show', 2020, 1, 'standard', 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, air_date, absolute_number, monitored, has_file, created_at, updated_at)
		 VALUES
		 ('ep_aired', 's1', 'se1', 1, 1, '2020-01-01', 1, 1, 0, 'now', 'now'),
		 ('ep_future', 's1', 'se1', 2, 1, '2099-01-01', 2, 1, 0, 'now', 'now'),
		 ('ep_empty', 's1', 'se1', 3, 1, '', 3, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	resp, err := m.ListMissing(ctx, &tvmgmtv1.ListMissingRequest{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 missing (aired+empty), got %d", resp.Total)
	}
	for _, it := range resp.Items {
		if it.EpisodeId == "ep_future" {
			t.Fatal("future air date must not be missing")
		}
	}
}

func TestMultiEpisodeFileLink(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Show', 2020, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, 1, 0, 'now', 'now'),
		        ('ep2', 's1', 'se1', 2, 1, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	resp, err := m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{
		EpisodeIds: []string{"ep1", "ep2"},
		FilePath:   "media/TV/Show/Season 01/Show.S01E01-E02.mkv",
		Quality:    "1080p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FileId == "" {
		t.Fatal("expected file id")
	}

	var has1, has2 int
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT has_file FROM episodes WHERE id='ep1'`).Scan(&has1)
	_ = m.db.QueryRow(`SELECT has_file FROM episodes WHERE id='ep2'`).Scan(&has2)
	var linkCount int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM episode_files WHERE file_path=?`, "media/TV/Show/Season 01/Show.S01E01-E02.mkv").Scan(&linkCount)
	m.mu.RUnlock()
	if has1 != 1 || has2 != 1 {
		t.Fatalf("both episodes should have file: %d %d", has1, has2)
	}
	if linkCount != 2 {
		t.Fatalf("expected 2 episode_files rows, got %d", linkCount)
	}

	_, err = m.RemoveEpisodeFile(ctx, &tvmgmtv1.RemoveEpisodeFileRequest{FileId: resp.FileId})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT has_file FROM episodes WHERE id='ep1'`).Scan(&has1)
	_ = m.db.QueryRow(`SELECT has_file FROM episodes WHERE id='ep2'`).Scan(&has2)
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM episode_files`).Scan(&linkCount)
	m.mu.RUnlock()
	if has1 != 0 || has2 != 0 || linkCount != 0 {
		t.Fatalf("remove should clear links: has=%d/%d count=%d", has1, has2, linkCount)
	}
}

func TestRenumberAbsoluteEpisodes(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, series_type, created_at, updated_at)
		 VALUES ('s1', 1, 'Anime', 2020, 1, 'anime', 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se0', 's1', 0, 1, 'now', 'now'), ('se1', 's1', 1, 1, 'now', 'now'), ('se2', 's1', 2, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, absolute_number, monitored, has_file, created_at, updated_at)
		 VALUES
		 ('sp', 's1', 'se0', 1, 0, 0, 1, 0, 'now', 'now'),
		 ('e1', 's1', 'se1', 1, 1, 0, 1, 0, 'now', 'now'),
		 ('e2', 's1', 'se1', 2, 1, 0, 1, 0, 'now', 'now'),
		 ('e3', 's1', 'se2', 1, 2, 0, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	m.renumberAbsoluteEpisodes(ctx, "s1")

	var a1, a2, a3, asp int
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT absolute_number FROM episodes WHERE id='e1'`).Scan(&a1)
	_ = m.db.QueryRow(`SELECT absolute_number FROM episodes WHERE id='e2'`).Scan(&a2)
	_ = m.db.QueryRow(`SELECT absolute_number FROM episodes WHERE id='e3'`).Scan(&a3)
	_ = m.db.QueryRow(`SELECT absolute_number FROM episodes WHERE id='sp'`).Scan(&asp)
	m.mu.RUnlock()
	if a1 != 1 || a2 != 2 || a3 != 3 {
		t.Fatalf("absolute numbering want 1,2,3 got %d,%d,%d", a1, a2, a3)
	}
	if asp != 0 {
		t.Fatalf("specials should stay 0, got %d", asp)
	}
	if id := m.findEpisodeIDByAbsolute("s1", 3); id != "e3" {
		t.Fatalf("find by absolute: got %q", id)
	}
}

func TestUpdateTVShowMonitoredCascades(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Show', 2020, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	mon := false
	_, err := m.UpdateTVShow(ctx, &tvmgmtv1.UpdateTVShowRequest{SeriesId: "s1", Monitored: &mon})
	if err != nil {
		t.Fatal(err)
	}
	var sm, em int
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT monitored FROM seasons WHERE id='se1'`).Scan(&sm)
	_ = m.db.QueryRow(`SELECT monitored FROM episodes WHERE id='ep1'`).Scan(&em)
	m.mu.RUnlock()
	if sm != 0 || em != 0 {
		t.Fatalf("cascade failed season=%d ep=%d", sm, em)
	}
}

func TestSeriesTypeValidation(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 9001, Name: "Bad", Year: 2020, SeriesType: "weekly",
	})
	if err == nil {
		t.Fatal("expected invalid series_type error")
	}

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 9002, Name: "Daily News", Year: 2020, SeriesType: "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if get.Series.SeriesType != "daily" {
		t.Fatalf("series_type=%q", get.Series.SeriesType)
	}
}

func TestDailyImportMatchByAirDate(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, series_type, created_at, updated_at)
		 VALUES ('s1', 1, 'Daily Show', 2020, 1, 'daily', 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, air_date, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, '2024-03-15', 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	ids := m.resolveImportEpisodeIDs(ctx, "s1", 0, contracts.FileImportedPayload{
		AirDate: "2024-03-15",
	})
	if len(ids) != 1 || ids[0] != "ep1" {
		t.Fatalf("daily match: %v", ids)
	}

	err := m.handleFileImported(ctx, contracts.FileImportedPayload{
		MediaType:  "tv",
		Title:      "Daily Show",
		TMDBID:     1,
		AirDate:    "2024-03-15",
		StorageKey: "media/TV/Daily Show/Season 01/ep.mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	var hasFile int
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT has_file FROM episodes WHERE id='ep1'`).Scan(&hasFile)
	m.mu.RUnlock()
	if hasFile != 1 {
		t.Fatalf("has_file=%d", hasFile)
	}
}

func TestTagsAndCalendar(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 42, Name: "Tagged", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := m.CreateTag(ctx, &tvmgmtv1.CreateTagRequest{Label: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.SetItemTags(ctx, &tvmgmtv1.SetItemTagsRequest{ItemId: add.SeriesId, TagIds: []string{tag.TagId}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{Page: 1, PageSize: 20, TagId: tag.TagId})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("tag filter total=%d", list.Total)
	}

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', ?, 1, 1, 'now', 'now')`, add.SeriesId)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, name, air_date, monitored, has_file, created_at, updated_at)
		 VALUES ('epc', ?, 'se1', 1, 1, 'Pilot', '2024-06-01', 1, 0, 'now', 'now')`, add.SeriesId)
	m.mu.Unlock()

	cal, err := m.GetCalendar(ctx, &tvmgmtv1.GetCalendarRequest{StartDate: "2024-06-01", EndDate: "2024-06-30"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Items) != 1 || cal.Items[0].EpisodeId != "epc" {
		t.Fatalf("calendar: %+v", cal.Items)
	}
}

func TestMediaAdminLibraryAdapters(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	s := mediaAdminServer{m: m}

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 42, Name: "Tagged", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.CreateTag(ctx, &mediaadminv1.CreateTagRequest{Label: "admin-anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemTags(ctx, &mediaadminv1.SetItemTagsRequest{
		ItemId: add.SeriesId, TagIds: []string{tag.TagId},
	}); err != nil {
		t.Fatal(err)
	}
	filtered, err := m.ListItems(ctx, &mediaadminv1.ListItemsRequest{Page: 1, PageSize: 20, TagId: tag.TagId})
	if err != nil || filtered.Total != 1 {
		t.Fatalf("tag filter: %+v %v", filtered, err)
	}

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', ?, 1, 1, 'now', 'now')`, add.SeriesId)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, name, air_date, monitored, has_file, created_at, updated_at)
		 VALUES ('epc', ?, 'se1', 1, 1, 'Pilot', '2024-06-01', 1, 0, 'now', 'now')`, add.SeriesId)
	m.mu.Unlock()

	missing, err := s.ListMissing(ctx, &mediaadminv1.ListMissingRequest{Page: 1, PageSize: 50, ParentId: add.SeriesId})
	if err != nil || missing.Total < 1 {
		t.Fatalf("admin missing: %+v %v", missing, err)
	}

	cal, err := s.GetCalendar(ctx, &mediaadminv1.GetCalendarRequest{StartDate: "2024-06-01", EndDate: "2024-06-30"})
	if err != nil || len(cal.Items) != 1 || cal.Items[0].Id != "epc" {
		t.Fatalf("admin calendar: %+v %v", cal, err)
	}

	if _, err := s.ListCollections(ctx, &mediaadminv1.ListCollectionsRequest{}); err == nil {
		t.Fatal("expected collections unimplemented for TV")
	}
}

func TestRemoveTVShowDeleteFiles(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "TV", "Show", "Season 01")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "Show.S01E01.mkv")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, root_folder_path, created_at, updated_at)
		 VALUES ('s1', 1, 'Show', 2020, 1, ?, 'now', 'now')`, root)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episode_files (id, episode_id, file_path, quality, size_bytes, container, created_at)
		 VALUES ('f1', 'ep1', ?, '1080p', 1, 'mkv', 'now')`, f)
	m.mu.Unlock()

	_, err := m.RemoveTVShow(ctx, &tvmgmtv1.RemoveTVShowRequest{SeriesId: "s1", DeleteFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("expected media file deleted")
	}
}

func TestLookupEpisode(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, original_name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1396, 'Breaking Bad', 'Breaking Bad', 2008, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 5, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, name, air_date, absolute_number, monitored, has_file, created_at, updated_at)
		 VALUES
		 ('ep1', 's1', 'se1', 1, 5, 'Live Free or Die', '2012-07-15', 50, 1, 0, 'now', 'now'),
		 ('ep2', 's1', 'se1', 2, 5, 'Madrigal', '2012-07-22', 51, 1, 0, 'now', 'now')`)
	m.mu.Unlock()

	resp, err := m.LookupEpisode(ctx, &tvmgmtv1.LookupEpisodeRequest{
		TmdbId: 1396, SeasonNumber: 5, EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Found || resp.EpisodeTitle != "Live Free or Die" || resp.AirDate != "2012-07-15" {
		t.Fatalf("lookup: %+v", resp)
	}
	if resp.SeriesName != "Breaking Bad" {
		t.Errorf("series name: %s", resp.SeriesName)
	}

	multi, err := m.LookupEpisode(ctx, &tvmgmtv1.LookupEpisodeRequest{
		TmdbId: 1396, SeasonNumber: 5, EpisodeNumbers: []int32{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !multi.Found || multi.EpisodeTitle != "Live Free or Die + Madrigal" {
		t.Fatalf("multi: %+v", multi)
	}

	miss, err := m.LookupEpisode(ctx, &tvmgmtv1.LookupEpisodeRequest{
		Title: "No Such Show", SeasonNumber: 1, EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if miss.Found {
		t.Fatal("expected not found")
	}
}

func TestPickBestSearchResultRequiresYear(t *testing.T) {
	results := []*metadatav1.SearchResult{
		{Id: 1585, Name: "Dragon Tales", FirstAirDate: "1999-09-06", Popularity: 99, MediaType: metadatav1.MediaType_MEDIA_TYPE_TV},
		{Id: 61865, Name: "When Calls the Heart", FirstAirDate: "2014-01-11", Popularity: 10, MediaType: metadatav1.MediaType_MEDIA_TYPE_TV},
	}
	got := pickBestSearchResult(results, 2014, false)
	if got == nil || got.GetId() != 61865 {
		t.Fatalf("want 2014 match, got %+v", got)
	}
	got = pickBestSearchResult(results[:1], 2014, false)
	if got != nil {
		t.Fatalf("year miss should not fall back, got %+v", got)
	}
}

func TestListTVShowsConcurrentWithEpisodePopulate(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})
	if err != nil {
		t.Fatal(err)
	}

	episodes := make([]*metadatav1.Episode, 80)
	for i := range episodes {
		episodes[i] = &metadatav1.Episode{
			Id:            int32(1000 + i),
			EpisodeNumber: int32(i + 1),
			SeasonNumber:  1,
			Name:          fmt.Sprintf("Episode %d", i+1),
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		seasonID := fmt.Sprintf("sea_%s_1", add.SeriesId)
		m.populateEpisodesFromSeason(ctx, add.SeriesId, seasonID, 1, episodes)
	}()

	listCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		select {
		case <-done:
			return
		case <-listCtx.Done():
			t.Fatal("ListTVShows blocked while episodes were populated")
		default:
			if _, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{Page: 1, PageSize: 20}); err != nil {
				t.Fatalf("ListTVShows: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestGetTVShowLoadsManyEpisodes(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1, Name: "Long Runner", Year: 2000})
	if err != nil {
		t.Fatal(err)
	}

	db := m.dbConn()
	if db == nil {
		t.Fatal("db not initialized")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	seasonID := fmt.Sprintf("sea_%s_1", add.SeriesId)
	_, err = db.ExecContext(ctx,
		`INSERT OR IGNORE INTO seasons (id, series_id, season_number, name, monitored, created_at, updated_at)
		 VALUES (?, ?, 1, 'Season 1', 1, ?, ?)`, seasonID, add.SeriesId, now, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 200; i++ {
		epID := fmt.Sprintf("ep_%s_1_%d", add.SeriesId, i)
		_, err = db.ExecContext(ctx,
			`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, name, monitored, has_file, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 1, ?, 1, 0, ?, ?)`,
			epID, add.SeriesId, seasonID, i, fmt.Sprintf("Episode %d", i), now, now)
		if err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("GetTVShow took %s for 200 episodes", time.Since(start))
	}
	if len(get.Series.Seasons) != 1 || len(get.Series.Seasons[0].Episodes) != 200 {
		t.Fatalf("expected 1 season with 200 episodes, got %d seasons / %d eps",
			len(get.Series.Seasons), len(get.Series.Seasons[0].Episodes))
	}
}
