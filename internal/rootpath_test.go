package internal

import (
	"context"
	"strings"
	"testing"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func TestRootFolderSoftUnavailable(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:         2001,
		Name:           "Soft Root",
		Year:           2020,
		RootFolderPath: "/unregistered/tv",
	})
	if err != nil {
		t.Fatal(err)
	}
	get, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if get.Series.RootFolderPath != "/unregistered/tv" {
		t.Errorf("path: %q", get.Series.RootFolderPath)
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
