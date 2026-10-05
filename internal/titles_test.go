package internal

import (
	"context"
	"testing"
	"time"

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

func TestSplitTitleYear(t *testing.T) {
	title, year := splitTitleYear("When Calls the Heart (2014)", 0)
	if title != "When Calls the Heart" || year != 2014 {
		t.Fatalf("got title=%q year=%d", title, year)
	}
	title, year = splitTitleYear("Breaking Bad (2008)", 0)
	if title != "Breaking Bad" || year != 2008 {
		t.Fatalf("got title=%q year=%d", title, year)
	}
	title, year = splitTitleYear("Game of Thrones", 2011)
	if title != "Game of Thrones" || year != 2011 {
		t.Fatalf("kept year: title=%q year=%d", title, year)
	}
	title, year = splitTitleYear("paddington bear 1989", 0)
	if title != "paddington bear" || year != 1989 {
		t.Fatalf("bare year: got title=%q year=%d", title, year)
	}
	title, year = splitTitleYear("Franklin 2024", 0)
	if title != "Franklin" || year != 2024 {
		t.Fatalf("reboot year: got title=%q year=%d", title, year)
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

// TestBackfillSeriesTitlesSingleConn runs the backfill on a single-connection
// pool. The old implementation upserted while its SELECT cursor was still open,
// which needs a second connection and therefore hangs with MaxOpenConns(1).
func TestBackfillSeriesTitlesSingleConn(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.db.SetMaxOpenConns(1)

	for i, name := range []string{"Alpha Show", "Beta Show"} {
		if _, err := m.db.ExecContext(ctx,
			`INSERT INTO series (id, tmdb_id, name, original_name, created_at, updated_at) VALUES (?, ?, ?, ?, 0, 0)`,
			"s"+name[:1], i+1, name, name+" Original"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM series_titles`); err != nil {
		t.Fatal(err)
	}

	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.backfillSeriesTitles(tctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("backfillSeriesTitles did not return (write inside open cursor)")
	}

	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM series_titles`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("series_titles rows = %d, want 4 (primary+original for 2 series)", n)
	}
}
