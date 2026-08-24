package internal

import (
	"context"
	"testing"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func TestHistoryImportDeleteAndList(t *testing.T) {
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

	fileResp, err := m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{
		EpisodeId: "ep1", FilePath: "/tv/Show.S01E01.mkv", Quality: "WEBDL-1080p",
	})
	if err != nil {
		t.Fatal(err)
	}

	hist, err := m.ListHistory(ctx, &mediaadminv1.ListHistoryRequest{Page: 1, PageSize: 20, ItemId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 || hist.Records[0].EventType != historyImport {
		t.Fatalf("expected 1 import, got total=%d type=%v", hist.Total, hist.Records)
	}

	if _, err := m.RemoveEpisodeFile(ctx, &tvmgmtv1.RemoveEpisodeFileRequest{FileId: fileResp.FileId}); err != nil {
		t.Fatal(err)
	}
	hist, err = m.ListHistory(ctx, &mediaadminv1.ListHistoryRequest{Page: 1, PageSize: 20, ItemId: "s1", EventType: historyDeleteFile})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 {
		t.Fatalf("expected 1 delete_file, got %d", hist.Total)
	}

	if _, err := m.RemoveTVShow(ctx, &tvmgmtv1.RemoveTVShowRequest{SeriesId: "s1"}); err != nil {
		t.Fatal(err)
	}
	hist, err = m.ListHistory(ctx, &mediaadminv1.ListHistoryRequest{Page: 1, PageSize: 20, EventType: historyDeleteItem})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 || hist.Records[0].ItemId != "s1" {
		t.Fatalf("expected delete_item for s1, got total=%d", hist.Total)
	}
}

func TestHistoryGrabFromDownloadDispatched(t *testing.T) {
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

	m.handleDownloadDispatched(ctx, contracts.DownloadDispatchedPayload{
		Title: "Show.S01E01", ItemType: "tv", ItemID: "ep1", Indexer: "nzb", DownloadID: "dl-tv",
	})
	m.handleDownloadDispatched(ctx, contracts.DownloadDispatchedPayload{
		Title: "Movie", ItemType: "movie", ItemID: "mv_1",
	})
	m.handleDownloadDispatched(ctx, contracts.DownloadDispatchedPayload{
		Title: "Show.S01", ItemType: "tv", ItemID: "s1:S1:pack", SeriesID: "s1", Indexer: "pack-idx",
	})

	hist, err := m.ListHistory(ctx, &mediaadminv1.ListHistoryRequest{Page: 1, PageSize: 20, EventType: historyGrab})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Total != 2 {
		t.Fatalf("expected 2 grabs, got %d", hist.Total)
	}
}
