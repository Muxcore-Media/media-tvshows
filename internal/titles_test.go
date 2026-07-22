package internal

import (
	"context"
	"testing"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func TestCleanMatchTitle(t *testing.T) {
	if got := cleanMatchTitle("Breaking.Bad"); got != "breaking bad" {
		t.Errorf("got %q", got)
	}
	if got := cleanMatchTitle("The Office"); got != "office" {
		t.Errorf("got %q", got)
	}
}

func TestFindSeriesByAlternateTitles(t *testing.T) {
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

	m.mu.Lock()
	m.upsertSeriesTitleLocked(ctx, add.SeriesId, "Metástasis", titleSourceTMDBAlt)
	m.mu.Unlock()

	if id, _ := m.findSeries(0, "Breaking.Bad", 2008); id != add.SeriesId {
		t.Errorf("scene title: got %q", id)
	}
	if id, _ := m.findSeries(0, "Metástasis", 2008); id != add.SeriesId {
		t.Errorf("tmdb alt: got %q", id)
	}
	if id, _ := m.findSeries(0, "Breaking Bad", 2010); id != "" {
		t.Errorf("year mismatch: got %q", id)
	}

	_, err = m.AddAlternateTitle(ctx, &tvmgmtv1.AddAlternateTitleRequest{
		SeriesId: add.SeriesId,
		Title:    "BrBa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := m.findSeries(0, "BrBa", 2008); id != add.SeriesId {
		t.Errorf("user alt: got %q", id)
	}
}
