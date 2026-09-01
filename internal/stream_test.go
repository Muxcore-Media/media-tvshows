package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func seedStreamEpisode(t *testing.T, m *Module, root, filePath string) string {
	t.Helper()
	ctx := context.Background()
	now := "2020-01-01T00:00:00Z"
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO series (id, tmdb_id, name, year, root_folder_path, monitored, created_at, updated_at)
		 VALUES ('s_stream', 1, 'Stream Show', 2020, ?, 1, ?, ?)`, root, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO seasons (id, series_id, season_number, monitored, created_at, updated_at)
		 VALUES ('se_stream', 's_stream', 1, 1, ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, monitored, has_file, created_at, updated_at)
		 VALUES ('ep_stream', 's_stream', 'se_stream', 1, 1, 1, 1, ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO episode_files (id, episode_id, file_path, quality, size_bytes, container, created_at)
		 VALUES ('f_stream', 'ep_stream', ?, '1080p', 100, 'mkv', ?)`, filePath, now)
	if err != nil {
		t.Fatal(err)
	}
	return "ep_stream"
}

func TestHandleStreamEpisodeUnauthorized(t *testing.T) {
	m := newTestModule(t)
	root := t.TempDir()
	filePath := filepath.Join(root, "show.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	seedStreamEpisode(t, m, root, filePath)

	t.Setenv("TVSHOWS_MODULE_TOKEN", "secret-stream-token")
	req := httptest.NewRequest(http.MethodGet, "/stream/tv/ep_stream", nil)
	rec := httptest.NewRecorder()
	m.handleStreamEpisode(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleStreamEpisodePathEscape(t *testing.T) {
	m := newTestModule(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.mkv")
	if err := os.WriteFile(outside, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	seedStreamEpisode(t, m, root, outside)

	t.Setenv("TVSHOWS_MODULE_TOKEN", "secret-stream-token")
	req := httptest.NewRequest(http.MethodGet, "/stream/tv/ep_stream", nil)
	req.Header.Set("Authorization", "Bearer secret-stream-token")
	rec := httptest.NewRecorder()
	m.handleStreamEpisode(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for path outside root, got %d", rec.Code)
	}
}

func TestHandleStreamEpisodeAuthorized(t *testing.T) {
	m := newTestModule(t)
	root := t.TempDir()
	filePath := filepath.Join(root, "show.mkv")
	if err := os.WriteFile(filePath, []byte("video-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	seedStreamEpisode(t, m, root, filePath)

	t.Setenv("TVSHOWS_MODULE_TOKEN", "secret-stream-token")
	req := httptest.NewRequest(http.MethodGet, "/stream/tv/ep_stream", nil)
	req.Header.Set("X-MuxCore-Module-Token", "secret-stream-token")
	rec := httptest.NewRecorder()
	m.handleStreamEpisode(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestGetTVShowExposesEpisodeFileFields(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	root := t.TempDir()
	filePath := filepath.Join(root, "S01E01.mkv")
	if err := os.WriteFile(filePath, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	seedStreamEpisode(t, m, root, filePath)

	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: "s_stream"})
	if err != nil {
		t.Fatal(err)
	}
	if len(get.Series.Seasons) != 1 || len(get.Series.Seasons[0].Episodes) != 1 {
		t.Fatal("expected one episode")
	}
	ep := get.Series.Seasons[0].Episodes[0]
	if ep.GetFileId() != "f_stream" || ep.GetFilePath() != filePath {
		t.Fatalf("file fields: id=%q path=%q", ep.GetFileId(), ep.GetFilePath())
	}
	if ep.GetQuality() != "1080p" || ep.GetSizeBytes() != 100 || ep.GetContainer() != "mkv" {
		t.Fatalf("quality/size/container: %q %d %q", ep.GetQuality(), ep.GetSizeBytes(), ep.GetContainer())
	}
}
