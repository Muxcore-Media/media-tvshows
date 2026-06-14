package internal

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
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
	t.Cleanup(func() { m.Stop(ctx) })
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
	if len(info.Capabilities) == 0 || info.Capabilities[0] != "media.library" {
		t.Errorf("expected media.library capability, got %v", info.Capabilities)
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

	m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})
	m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1668, Name: "Breaking Bad", Year: 2008})

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
		m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
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

	m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1, Name: "Breaking Bad", Year: 2008})
	m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 2, Name: "Better Call Saul", Year: 2015})
	m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 3, Name: "Inception", Year: 2010})

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
}

func TestListItems(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1, Name: "Test Show", Year: 2020})

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
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Item.Title != "Breaking Bad (Updated)" {
		t.Errorf("expected updated title, got %s", updated.Item.Title)
	}
}

func TestListArtwork(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	mod := m
	mod.mu.Lock()
	mod.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, poster_path, backdrop_path, monitored, created_at, updated_at)
		 VALUES ('test123', 1, 'Test', 2020, '/poster.jpg', '/backdrop.jpg', 1, 'now', 'now')`)
	mod.mu.Unlock()

	resp, err := m.ListArtwork(ctx, &mediaadminv1.ListArtworkRequest{Id: "test123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Artwork) != 2 {
		t.Fatalf("expected 2 artwork entries, got %d", len(resp.Artwork))
	}
	if resp.Artwork[0].Type != "poster" {
		t.Errorf("expected first artwork type 'poster', got %s", resp.Artwork[0].Type)
	}
}

func TestUpdateEpisodeMonitored(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	mod := m
	mod.mu.Lock()
	mod.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Test', 2020, 1, 'now', 'now')`)
	mod.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	mod.db.ExecContext(ctx,
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
	mod.db.QueryRowContext(ctx, `SELECT monitored FROM episodes WHERE id = 'ep1'`).Scan(&monitored)
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
	mod.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Test', 2020, 1, 'now', 'now')`)
	mod.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	mod.db.ExecContext(ctx,
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
	mod.db.QueryRowContext(ctx, `SELECT monitored FROM seasons WHERE id = 'se1'`).Scan(&monitored)
	mod.mu.RUnlock()
	if monitored != 0 {
		t.Errorf("expected season monitored=0, got %d", monitored)
	}

	mod.mu.RLock()
	mod.db.QueryRowContext(ctx, `SELECT monitored FROM episodes WHERE id = 'ep1'`).Scan(&monitored)
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
