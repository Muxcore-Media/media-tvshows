package internal

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func TestSearchIndexersNotFound(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.SearchIndexers(ctx, &mediaadminv1.SearchIndexersRequest{ItemId: "missing"})
	if err == nil {
		t.Fatal("expected not found")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestSearchIndexersNilMeshReturnsEmpty(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 1399,
		Name:   "Game of Thrones",
		Year:   2011,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := m.SearchIndexers(ctx, &mediaadminv1.SearchIndexersRequest{ItemId: add.SeriesId})
	if err != nil {
		t.Fatalf("expected soft empty result, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.GetResults()) != 0 || resp.GetTotal() != 0 {
		t.Fatalf("expected empty results, got %+v", resp)
	}
}

func TestReleaseMatchToIndexerResult(t *testing.T) {
	match := &automationv1.ReleaseMatch{
		Guid:        "guid-tv-1",
		Title:       "Game of Thrones S01 2160p WEB-DL",
		Size:        12_000_000_000,
		Seeders:     10,
		IndexerName: "TestIndexer",
		DownloadUrl: "magnet:?xt=urn:btih:tv",
		Score:       80,
	}
	got := releaseMatchToIndexerResult(match)
	if got.GetQuality() != "2160p" {
		t.Errorf("quality: got %q want 2160p", got.GetQuality())
	}
	if !got.GetApproved() {
		t.Error("expected approved=true")
	}
}

func TestSearchIndexersUsesAutomationStub(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId: 1399,
		Name:   "Game of Thrones",
		Year:   2011,
	})
	if err != nil {
		t.Fatal(err)
	}

	var captured *automationv1.SearchItemRequest
	m.automationSearchFn = func(ctx context.Context, req *automationv1.SearchItemRequest) (*automationv1.SearchItemResponse, error) {
		captured = req
		return &automationv1.SearchItemResponse{
			Matches: []*automationv1.ReleaseMatch{{
				Guid:        "g-tv",
				Title:       "Game of Thrones S01 1080p",
				IndexerName: "StubIndexer",
				DownloadUrl: "magnet:test",
			}},
		}, nil
	}

	resp, err := m.SearchIndexers(ctx, &mediaadminv1.SearchIndexersRequest{ItemId: add.SeriesId})
	if err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("expected automation SearchItem to be called")
	}
	if captured.GetItemType() != "tv" {
		t.Errorf("item_type: got %q want tv", captured.GetItemType())
	}
	if captured.GetQuery() != "Game of Thrones" {
		t.Errorf("query: got %q want Game of Thrones", captured.GetQuery())
	}
	if captured.GetSeason() != 0 || captured.GetEpisode() != 0 {
		t.Errorf("expected series-level search season=0 episode=0, got season=%d episode=%d",
			captured.GetSeason(), captured.GetEpisode())
	}
	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}
}
