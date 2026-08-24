package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
)

func TestEpisodeStillListArtwork(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	relStill := "ep1/still.jpg"
	if err := os.MkdirAll(filepath.Join(m.imageDir, "ep1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.imageDir, relStill), []byte("still"), 0o600); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, monitored, created_at, updated_at)
		 VALUES ('s1', 1, 'Test', 2020, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se1', 's1', 1, 1, 'now', 'now')`)
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, tmdb_id, episode_number, season_number, still_path, monitored, has_file, created_at, updated_at)
		 VALUES ('ep1', 's1', 'se1', 1, 1, 1, ?, 1, 1, 'now', 'now')`, relStill)
	m.mu.Unlock()

	resp, err := m.ListArtwork(ctx, &mediaadminv1.ListArtworkRequest{Id: "ep1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Artwork) != 1 {
		t.Fatalf("expected 1 still, got %d", len(resp.Artwork))
	}
	if resp.Artwork[0].Type != "still" {
		t.Fatalf("expected still, got %s", resp.Artwork[0].Type)
	}
	if !strings.Contains(resp.Artwork[0].Url, "/images/"+relStill) {
		t.Fatalf("unexpected url %s", resp.Artwork[0].Url)
	}
}
