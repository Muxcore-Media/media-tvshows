package internal

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	"github.com/Muxcore-Media/core/sdk/go/module/moduletest"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

// upgradeSnapshots lists committed snapshots produced by the named tag's own
// code (ADR-0015). See testdata/upgrade/README.md.
var upgradeSnapshots = []string{"v0.1.9", "v0.1.20"}

func openUpgradeModule(t *testing.T, dbPath string) *Module {
	t.Helper()
	m := NewModule(Config{
		DBPath:   dbPath,
		ImageDir: filepath.Join(t.TempDir(), "images"),
		GRPCAddr: "127.0.0.1:0",
		HTTPAddr: "127.0.0.1:0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init(%s): %v", dbPath, err)
	}
	return m
}

func closeUpgradeModule(t *testing.T, m *Module) {
	t.Helper()
	if m.grpcLis != nil {
		_ = m.grpcLis.Close()
	}
	if m.httpLis != nil {
		_ = m.httpLis.Close()
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestUpgradeFromSnapshots(t *testing.T) {
	for _, tag := range upgradeSnapshots {
		t.Run(tag, func(t *testing.T) {
			fixture := filepath.Join("testdata", "upgrade", tag+".db")
			ctx := context.Background()

			freshMod := openUpgradeModule(t, filepath.Join(t.TempDir(), "fresh.db"))
			defer closeUpgradeModule(t, freshMod)
			freshSchema := moduletest.Schema(t, freshMod.dbConn())

			// The first open performs the upgrade; the second proves
			// startup against an already-upgraded database is idempotent.
			path := moduletest.CopyFixture(t, fixture)
			for pass := 1; pass <= 2; pass++ {
				m := openUpgradeModule(t, path)
				db := m.dbConn()
				upSchema := moduletest.Schema(t, db)
				moduletest.RequireSchemaSuperset(t, upSchema, freshSchema)
				requireColumnDefaults(t, upSchema, freshSchema)
				requireSeededRows(t, ctx, m)
				moduletest.RequireIntegrity(t, db)
				closeUpgradeModule(t, m)
			}
		})
	}
}

// requireColumnDefaults asserts that every column the current code defines has
// the same NOT NULL and DEFAULT contract after upgrade as in a fresh database,
// so columns added by ALTER TABLE get usable defaults for pre-existing rows.
func requireColumnDefaults(t *testing.T, upgraded, fresh moduletest.SchemaInfo) {
	t.Helper()
	for name, ft := range fresh.Tables {
		ut := upgraded.Tables[name]
		have := map[string]moduletest.Column{}
		for _, c := range ut.Columns {
			have[c.Name] = c
		}
		for _, fc := range ft.Columns {
			uc, ok := have[fc.Name]
			if !ok {
				continue // reported by RequireSchemaSuperset
			}
			if uc.HasDflt != fc.HasDflt || uc.Default != fc.Default || uc.NotNull != fc.NotNull {
				t.Errorf("column %s.%s: upgraded (notnull=%v default=%q has=%v), fresh (notnull=%v default=%q has=%v)",
					name, fc.Name, uc.NotNull, uc.Default, uc.HasDflt, fc.NotNull, fc.Default, fc.HasDflt)
			}
		}
	}
}

func requireSeededRows(t *testing.T, ctx context.Context, m *Module) {
	t.Helper()
	db := m.dbConn()

	for table, want := range map[string]int{
		"series": 2, "seasons": 3, "episodes": 4, "episode_files": 2,
		"tags": 2, "item_tags": 3, "history": 3, "series_titles": 4,
	} {
		if got := countRows(t, db, table); got != want {
			t.Errorf("%s: %d rows after upgrade, want %d (data loss or duplication)", table, got, want)
		}
	}

	// series, seasons, episodes through the store API
	got, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: "ser_1"})
	if err != nil {
		t.Fatalf("GetTVShow ser_1: %v", err)
	}
	s := got.GetSeries()
	if s.GetName() != "Breaking Bad" || s.GetTmdbId() != 1668 || s.GetYear() != 2008 ||
		s.GetStatus() != "Ended" || s.GetNetwork() != "AMC" || s.GetVoteAverage() != 8.9 ||
		s.GetTagline() != "Change the equation" || !s.GetMonitored() ||
		s.GetTotalSeasons() != 2 || s.GetTotalEpisodes() != 3 ||
		s.GetQualityProfileId() != "qp_hd" || s.GetRootFolderPath() != "/media/tv/archive" ||
		s.GetSeriesType() != "standard" || s.GetPosterPath() != "/poster1.jpg" ||
		s.GetCreatedAt() != "2026-01-01T10:00:00Z" {
		t.Errorf("ser_1 mismatch: %+v", s)
	}
	if g := s.GetGenres(); len(g) != 2 || g[0] != "Drama" || g[1] != "Crime" {
		t.Errorf("ser_1 genres = %v", g)
	}
	if len(s.GetSeasons()) != 2 {
		t.Fatalf("ser_1 seasons = %d, want 2", len(s.GetSeasons()))
	}
	s1, s2 := s.GetSeasons()[0], s.GetSeasons()[1]
	if s1.GetSeasonNumber() != 1 || s1.GetName() != "Season 1" || !s1.GetMonitored() || len(s1.GetEpisodes()) != 2 {
		t.Errorf("season 1 mismatch: %+v", s1)
	}
	if s2.GetSeasonNumber() != 2 || s2.GetMonitored() || len(s2.GetEpisodes()) != 1 {
		t.Errorf("season 2 mismatch: %+v", s2)
	}
	if len(s1.GetEpisodes()) == 2 {
		e := s1.GetEpisodes()[0]
		if e.GetId() != "ep_1" || e.GetName() != "Pilot" || e.GetTmdbId() != 62085 ||
			e.GetAbsoluteNumber() != 1 || !e.GetHasFile() || !e.GetMonitored() || e.GetStillPath() != "/still1.jpg" {
			t.Errorf("ep_1 mismatch: %+v", e)
		}
		if e2 := s1.GetEpisodes()[1]; e2.GetId() != "ep_2" || e2.GetHasFile() {
			t.Errorf("ep_2 mismatch: %+v", e2)
		}
	}

	got2, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: "ser_2"})
	if err != nil {
		t.Fatalf("GetTVShow ser_2: %v", err)
	}
	s = got2.GetSeries()
	if s.GetName() != "One Piece" || s.GetOriginalName() != "ONE PIECE" || s.GetMonitored() ||
		s.GetSeriesType() != "anime" || s.GetRootFolderPath() != "/home/alice/anime" {
		t.Errorf("ser_2 mismatch: %+v", s)
	}

	requireUnavailableClassification(t, ctx, m)

	// episode files (personal paths)
	for ep, want := range map[string]string{
		"ep_1": "/media/tv/archive/Breaking Bad/S01E01.mkv",
		"ep_4": "/home/alice/anime/One Piece/S01E01.mp4",
	} {
		path, fileID, quality, ok := m.lookupEpisodeFile(ctx, ep)
		if !ok || path != want || fileID == "" || quality == "" {
			t.Errorf("episode file %s = (%q, %q, %q, %v), want path %q", ep, path, fileID, quality, ok, want)
		}
	}
	var size int64
	if err := db.QueryRowContext(ctx, `SELECT size_bytes FROM episode_files WHERE id = 'ef_1'`).Scan(&size); err != nil || size != 2147483648 {
		t.Errorf("ef_1 size_bytes = %d, %v", size, err)
	}

	// tags
	tags, err := m.ListTags(ctx, &tvmgmtv1.ListTagsRequest{})
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags.GetTags()) != 2 || tags.GetTags()[0].GetLabel() != "favorites" || tags.GetTags()[1].GetLabel() != "kids-ok" {
		t.Errorf("tags = %v", tags.GetTags())
	}
	it, err := m.GetItemTags(ctx, &tvmgmtv1.GetItemTagsRequest{ItemId: "ser_1"})
	if err != nil {
		t.Fatalf("GetItemTags: %v", err)
	}
	if len(it.GetTags()) != 2 {
		t.Errorf("ser_1 tags = %v, want 2", it.GetTags())
	}

	// history
	h, err := m.ListHistory(ctx, &mediaadminv1.ListHistoryRequest{})
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if h.GetTotal() != 3 || len(h.GetRecords()) != 3 {
		t.Fatalf("history total=%d records=%d, want 3", h.GetTotal(), len(h.GetRecords()))
	}
	byID := map[string]*mediaadminv1.HistoryRecord{}
	for _, r := range h.GetRecords() {
		byID[r.GetId()] = r
	}
	if r := byID["th_1"]; r == nil || r.GetEventType() != mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_GRAB ||
		r.GetIndexer() != "fixture-indexer" || r.GetDownloadId() != "dl_abc123" ||
		r.GetSourceTitle() != "Breaking.Bad.S01E01.1080p.BluRay" {
		t.Errorf("th_1 mismatch: %v", r)
	}
	if r := byID["th_3"]; r == nil || r.GetEventType() != mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_DELETE_FILE ||
		r.GetFilePath() != "/home/alice/anime/One Piece/old.mp4" {
		t.Errorf("th_3 mismatch: %v", r)
	}
	var dataJSON string
	if err := db.QueryRowContext(ctx, `SELECT data_json FROM history WHERE id = 'th_1'`).Scan(&dataJSON); err != nil ||
		dataJSON != `{"download_protocol":"torrent","tmdb_id":1668}` {
		t.Errorf("th_1 data_json = %q, %v", dataJSON, err)
	}

	// series_titles survive and keep their sources
	var alias string
	if err := db.QueryRowContext(ctx, `SELECT title FROM series_titles WHERE id = 'st_4' AND source = 'alias' AND series_id = 'ser_1'`).Scan(&alias); err != nil || alias != "BB" {
		t.Errorf("st_4 alias = %q, %v", alias, err)
	}
}

// requireUnavailableClassification asserts the ADR-0031 migration contract: a
// pre-existing database has no parental classification, every series reads as
// "unavailable" (empty rating, empty source), nothing is inferred from other
// data (ser_1 has vote_average 8.9 and a "kids-ok" tag), episodes still resolve
// to their series, and an enabled filter shows none of them.
func requireUnavailableClassification(t *testing.T, ctx context.Context, m *Module) {
	t.Helper()

	wantLabels := map[string][]string{
		"ser_1": {"favorites", "kids-ok"},
		"ser_2": {"favorites"},
	}
	list, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{})
	if err != nil {
		t.Fatalf("ListTVShows: %v", err)
	}
	if list.GetTotal() != 2 || len(list.GetSeries()) != 2 {
		t.Fatalf("list total=%d len=%d, want 2", list.GetTotal(), len(list.GetSeries()))
	}
	for _, s := range list.GetSeries() {
		if s.GetContentRating() != "" || s.GetContentRatingSource() != "" {
			t.Errorf("%s: rating=%q source=%q, want unavailable (empty, empty)", s.GetId(), s.GetContentRating(), s.GetContentRatingSource())
		}
		if got := s.GetTagLabels(); !slices.Equal(got, wantLabels[s.GetId()]) {
			t.Errorf("%s: tag_labels = %v, want %v", s.GetId(), got, wantLabels[s.GetId()])
		}
	}

	got, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: "ser_1"})
	if err != nil {
		t.Fatalf("GetTVShow ser_1: %v", err)
	}
	if s := got.GetSeries(); s.GetContentRating() != "" || s.GetContentRatingSource() != "" ||
		!slices.Equal(s.GetTagLabels(), wantLabels["ser_1"]) {
		t.Errorf("ser_1 detail classification = %q/%q/%v", s.GetContentRating(), s.GetContentRatingSource(), s.GetTagLabels())
	}

	for ep, series := range map[string]string{"ep_1": "ser_1", "ep_3": "ser_1", "ep_4": "ser_2"} {
		r, err := m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: ep})
		if err != nil || r.GetEpisode().GetSeriesId() != series {
			t.Errorf("GetEpisode(%s) = %v, %v; want series %s", ep, r.GetEpisode(), err, series)
		}
	}

	// Most permissive enabled filter: unavailable is hidden regardless.
	filtered, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{
		ClassificationFilter: &tvmgmtv1.ClassificationFilter{Enabled: true, AllowUnrated: true},
	})
	if err != nil {
		t.Fatalf("ListTVShows filtered: %v", err)
	}
	if filtered.GetTotal() != 0 || len(filtered.GetSeries()) != 0 {
		t.Errorf("filter over upgraded db: total=%d len=%d, want 0 (all unavailable)", filtered.GetTotal(), len(filtered.GetSeries()))
	}
}
