package internal

import (
	"context"
	"testing"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func TestExportImportStateRoundTrip(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 4242,
		Name:   "Backup Show",
		Year:   2024,
		Genres: []string{"Drama"},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := m.ExportState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) == 0 {
		t.Fatal("expected non-empty export")
	}

	if _, err := m.RemoveTVShow(ctx, &tvmgmtv1.RemoveTVShowRequest{SeriesId: add.SeriesId}); err != nil {
		t.Fatal(err)
	}
	list, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 0 {
		t.Fatalf("expected empty library before import, got %d", list.Total)
	}

	if err := m.ImportState(ctx, snap); err != nil {
		t.Fatal(err)
	}

	list, err = m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Series[0].GetName() != "Backup Show" {
		t.Fatalf("expected restored series, got %+v", list)
	}
}
