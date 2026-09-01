package internal

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
)

const capMediaAutomation = "media.automation"

type indexerSearchContext struct {
	title            string
	year             int32
	tmdbID           int32
	qualityProfileID string
	season           int32
	episode          int32
	absolute         int32
	seriesType       string
}

func (m *Module) SearchIndexers(ctx context.Context, req *mediaadminv1.SearchIndexersRequest) (*mediaadminv1.SearchIndexersResponse, error) {
	if req.GetItemId() == "" {
		return nil, status.Error(codes.InvalidArgument, "item_id required")
	}

	searchCtx, err := m.lookupItemForIndexerSearch(ctx, req.GetItemId())
	if err != nil {
		return nil, err
	}

	limit := req.GetLimit()
	if limit <= 0 {
		limit = 50
	}

	searchResp, err := m.automationSearchItem(ctx, &automationv1.SearchItemRequest{
		ItemType:         "tv",
		Query:            searchCtx.title,
		TmdbId:           searchCtx.tmdbID,
		Year:             searchCtx.year,
		Season:           searchCtx.season,
		Episode:          searchCtx.episode,
		Absolute:         searchCtx.absolute,
		SeriesType:       searchCtx.seriesType,
		Limit:            limit,
		QualityProfileId: searchCtx.qualityProfileID,
	})
	if err != nil {
		return emptySearchIndexersResponse(), nil
	}

	results := releaseMatchesToIndexerResults(searchResp.GetMatches())
	return &mediaadminv1.SearchIndexersResponse{
		Results: results,
		Total:   int32(len(results)),
	}, nil
}

func (m *Module) lookupItemForIndexerSearch(ctx context.Context, itemID string) (indexerSearchContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return indexerSearchContext{}, fmt.Errorf("not initialized")
	}

	var out indexerSearchContext
	var y, tmdb int64
	err := m.db.QueryRowContext(ctx,
		`SELECT name, year, tmdb_id, quality_profile_id, COALESCE(series_type, 'standard') FROM series WHERE id = ?`, itemID,
	).Scan(&out.title, &y, &tmdb, &out.qualityProfileID, &out.seriesType)
	if err == nil {
		out.year = int32(y)
		out.tmdbID = int32(tmdb)
		return out, nil
	}

	err = m.db.QueryRowContext(ctx,
		`SELECT s.name, s.year, s.tmdb_id, s.quality_profile_id, COALESCE(s.series_type, 'standard'),
		        e.season_number, e.episode_number, e.absolute_number
		 FROM episodes e
		 INNER JOIN series s ON s.id = e.series_id
		 WHERE e.id = ?`, itemID,
	).Scan(&out.title, &y, &tmdb, &out.qualityProfileID, &out.seriesType,
		&out.season, &out.episode, &out.absolute)
	if err != nil {
		return indexerSearchContext{}, status.Errorf(codes.NotFound, "item not found: %s", itemID)
	}
	out.year = int32(y)
	out.tmdbID = int32(tmdb)
	return out, nil
}

func (m *Module) automationSearchItem(ctx context.Context, req *automationv1.SearchItemRequest) (*automationv1.SearchItemResponse, error) {
	if m.automationSearchFn != nil {
		return m.automationSearchFn(ctx, req)
	}
	if m.mc == nil {
		return nil, fmt.Errorf("not connected to core")
	}
	addr, err := m.findAutomationAddr(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(addr, meshGRPCDialOpts()...)
	if err != nil {
		return nil, fmt.Errorf("dial automation: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return automationv1.NewAutomationServiceClient(conn).SearchItem(ctx, req)
}

func (m *Module) findAutomationAddr(ctx context.Context) (string, error) {
	if m.mc == nil {
		return "", fmt.Errorf("not connected to core")
	}
	modules, err := m.mc.Discovery.FindByCapability(ctx, capMediaAutomation)
	if err != nil {
		return "", fmt.Errorf("discover %s: %w", capMediaAutomation, err)
	}
	for _, mod := range modules {
		addr := dialAddrForModule(mod.Id, mod.HttpAddr)
		if addr != "" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no %s module found", capMediaAutomation)
}

func meshGRPCDialOpts() []grpc.DialOption {
	return []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
}

func emptySearchIndexersResponse() *mediaadminv1.SearchIndexersResponse {
	return &mediaadminv1.SearchIndexersResponse{}
}

func releaseMatchesToIndexerResults(matches []*automationv1.ReleaseMatch) []*mediaadminv1.IndexerResult {
	results := make([]*mediaadminv1.IndexerResult, 0, len(matches))
	for _, match := range matches {
		if match == nil {
			continue
		}
		results = append(results, releaseMatchToIndexerResult(match))
	}
	return results
}

func releaseMatchToIndexerResult(match *automationv1.ReleaseMatch) *mediaadminv1.IndexerResult {
	return &mediaadminv1.IndexerResult{
		Title:       match.GetTitle(),
		Guid:        match.GetGuid(),
		Indexer:     match.GetIndexerName(),
		Size:        match.GetSize(),
		Quality:     qualityFromReleaseTitle(match.GetTitle()),
		Seeders:     match.GetSeeders(),
		DownloadUrl: match.GetDownloadUrl(),
		Approved:    true,
	}
}

func qualityFromReleaseTitle(title string) string {
	upper := strings.ToUpper(title)
	for _, q := range []struct {
		needle string
		out    string
	}{
		{"2160P", "2160p"},
		{"1080P", "1080p"},
		{"720P", "720p"},
		{"480P", "480p"},
		{"4K", "2160p"},
	} {
		if strings.Contains(upper, q.needle) {
			return q.out
		}
	}
	return ""
}
