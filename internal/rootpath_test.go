package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func TestRootFolderFailsClosedWhenRootsUnavailable(t *testing.T) {
	m := newTestModule(t)
	m.rootsListFn = nil // no mesh in unit tests => roots unavailable
	ctx := context.Background()

	_, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 1001, Name: "Closed Root", Year: 2020, RootFolderPath: "/unregistered/tv",
	})
	if err == nil || !strings.Contains(err.Error(), "refusing path") {
		t.Fatalf("expected fail-closed error, got %v", err)
	}
	// Empty root stays allowed.
	if _, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 1002, Name: "No Root", Year: 2020}); err != nil {
		t.Fatal(err)
	}
	// File paths are refused too.

	if _, err := m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{EpisodeId: "epX", FilePath: "/media/f.mkv"}); err == nil {
		t.Fatal("AddFile must fail closed when roots are unavailable")
	}
}

func TestAddFileConfinedToRegisteredRoots(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	root := t.TempDir()
	outside := t.TempDir()
	// sibling-prefix directory: <root>-evil
	evil := root + "-evil"
	if err := os.MkdirAll(evil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(evil) })
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	setRoots(m, root)
	_, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{TmdbId: 2001, Name: "C", Year: 2020, RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"traversal":       root + "/../etc/passwd",
		"sibling prefix":  filepath.Join(evil, "a.mkv"),
		"symlink escape":  filepath.Join(root, "link", "a.mkv"),
		"relative-dotdot": "movies/../../a.mkv",
		"outside":         filepath.Join(outside, "a.mkv"),
	}
	for name, p := range cases {
		if _, err := m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{EpisodeId: "epX", FilePath: p}); err == nil {
			t.Errorf("%s: AddFile(%q) must be rejected", name, p)
		}
	}
	if _, err := m.AddEpisodeFile(ctx, &tvmgmtv1.AddEpisodeFileRequest{EpisodeId: "epX", FilePath: filepath.Join(root, "ok", "a.mkv")}); err != nil {
		t.Errorf("in-root file rejected: %v", err)
	}
}
func TestRootFolderRejectsUnknownWhenAvailable(t *testing.T) {
	m := newTestModule(t)
	m.rootsListFn = func(ctx context.Context, mediaKind string) ([]string, error) {
		return []string{"/media/tv"}, nil
	}
	_, err := m.AddTVShow(context.Background(), &tvmgmtv1.AddTVShowRequest{
		TmdbId:         2002,
		Name:           "Bad Root",
		Year:           2021,
		RootFolderPath: "/elsewhere",
	})
	if err == nil || !strings.Contains(err.Error(), "not a registered root") {
		t.Fatalf("expected registered root error, got %v", err)
	}
}

func TestRootFolderAcceptsRegistered(t *testing.T) {
	m := newTestModule(t)
	m.rootsListFn = func(ctx context.Context, mediaKind string) ([]string, error) {
		if mediaKind != "tv" {
			t.Errorf("mediaKind: %q", mediaKind)
		}
		return []string{"/media/tv"}, nil
	}
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:         2003,
		Name:           "Good Root",
		Year:           2022,
		RootFolderPath: "/media/tv/",
	})
	if err != nil {
		t.Fatal(err)
	}
	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if get.Series.RootFolderPath != "/media/tv" {
		t.Errorf("normalized path: %q", get.Series.RootFolderPath)
	}
}
