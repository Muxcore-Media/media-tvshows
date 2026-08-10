package internal

import (
	"context"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

// mediaAdminServer adapts Module to MediaAdminService without colliding with
// TvManagementService method names (ListMissing, CreateTag, …).
type mediaAdminServer struct {
	mediaadminv1.UnimplementedMediaAdminServiceServer
	m *Module
}

func (s mediaAdminServer) GetMediaTypeInfo(ctx context.Context, req *mediaadminv1.GetMediaTypeInfoRequest) (*mediaadminv1.GetMediaTypeInfoResponse, error) {
	return s.m.GetMediaTypeInfo(ctx, req)
}

func (s mediaAdminServer) ListItems(ctx context.Context, req *mediaadminv1.ListItemsRequest) (*mediaadminv1.ListItemsResponse, error) {
	return s.m.ListItems(ctx, req)
}

func (s mediaAdminServer) GetItem(ctx context.Context, req *mediaadminv1.GetItemRequest) (*mediaadminv1.GetItemResponse, error) {
	return s.m.GetItem(ctx, req)
}

func (s mediaAdminServer) UpdateMetadata(ctx context.Context, req *mediaadminv1.UpdateMetadataRequest) (*mediaadminv1.UpdateMetadataResponse, error) {
	return s.m.UpdateMetadata(ctx, req)
}

func (s mediaAdminServer) ListArtwork(ctx context.Context, req *mediaadminv1.ListArtworkRequest) (*mediaadminv1.ListArtworkResponse, error) {
	return s.m.ListArtwork(ctx, req)
}

func (s mediaAdminServer) ReplaceArtwork(stream mediaadminv1.MediaAdminService_ReplaceArtworkServer) error {
	return s.m.ReplaceArtwork(stream)
}

func (s mediaAdminServer) DeleteItem(ctx context.Context, req *mediaadminv1.DeleteItemRequest) (*mediaadminv1.DeleteItemResponse, error) {
	return s.m.DeleteItem(ctx, req)
}

func (s mediaAdminServer) RefreshItem(ctx context.Context, req *mediaadminv1.RefreshItemRequest) (*mediaadminv1.RefreshItemResponse, error) {
	return s.m.RefreshItem(ctx, req)
}

func (s mediaAdminServer) SearchIndexers(ctx context.Context, req *mediaadminv1.SearchIndexersRequest) (*mediaadminv1.SearchIndexersResponse, error) {
	return s.m.SearchIndexers(ctx, req)
}

func (s mediaAdminServer) ListHistory(ctx context.Context, req *mediaadminv1.ListHistoryRequest) (*mediaadminv1.ListHistoryResponse, error) {
	return s.m.ListHistory(ctx, req)
}

func (s mediaAdminServer) ListMissing(ctx context.Context, req *mediaadminv1.ListMissingRequest) (*mediaadminv1.ListMissingResponse, error) {
	resp, err := s.m.ListMissing(ctx, &tvmgmtv1.ListMissingRequest{
		Page: req.GetPage(), PageSize: req.GetPageSize(), SeriesId: req.GetParentId(),
	})
	if err != nil {
		return nil, err
	}
	items := make([]*mediaadminv1.MissingItem, 0, len(resp.GetItems()))
	for _, it := range resp.GetItems() {
		items = append(items, &mediaadminv1.MissingItem{
			Id: it.GetEpisodeId(), ParentId: it.GetSeriesId(),
			Title: it.GetTitle(), Year: it.GetYear(),
			Metadata: map[string]string{
				"tmdb_id":            strconv.Itoa(int(it.GetTmdbId())),
				"season_number":      strconv.Itoa(int(it.GetSeasonNumber())),
				"episode_number":     strconv.Itoa(int(it.GetEpisodeNumber())),
				"quality_profile_id": it.GetQualityProfileId(),
				"air_date":           it.GetAirDate(),
				"absolute_number":    strconv.Itoa(int(it.GetAbsoluteNumber())),
				"series_type":        it.GetSeriesType(),
			},
		})
	}
	return &mediaadminv1.ListMissingResponse{
		Items: items, Total: resp.GetTotal(),
		Page: resp.GetPage(), PageSize: resp.GetPageSize(),
	}, nil
}

func (s mediaAdminServer) ListTags(ctx context.Context, req *mediaadminv1.ListTagsRequest) (*mediaadminv1.ListTagsResponse, error) {
	resp, err := s.m.ListTags(ctx, &tvmgmtv1.ListTagsRequest{})
	if err != nil {
		return nil, err
	}
	tags := make([]*mediaadminv1.Tag, 0, len(resp.GetTags()))
	for _, t := range resp.GetTags() {
		tags = append(tags, &mediaadminv1.Tag{
			Id: t.GetId(), Label: t.GetLabel(), CreatedAt: t.GetCreatedAt(),
		})
	}
	return &mediaadminv1.ListTagsResponse{Tags: tags}, nil
}

func (s mediaAdminServer) CreateTag(ctx context.Context, req *mediaadminv1.CreateTagRequest) (*mediaadminv1.CreateTagResponse, error) {
	resp, err := s.m.CreateTag(ctx, &tvmgmtv1.CreateTagRequest{Label: req.GetLabel()})
	if err != nil {
		return nil, err
	}
	return &mediaadminv1.CreateTagResponse{TagId: resp.GetTagId()}, nil
}

func (s mediaAdminServer) DeleteTag(ctx context.Context, req *mediaadminv1.DeleteTagRequest) (*mediaadminv1.DeleteTagResponse, error) {
	_, err := s.m.DeleteTag(ctx, &tvmgmtv1.DeleteTagRequest{TagId: req.GetTagId()})
	if err != nil {
		return nil, err
	}
	return &mediaadminv1.DeleteTagResponse{}, nil
}

func (s mediaAdminServer) SetItemTags(ctx context.Context, req *mediaadminv1.SetItemTagsRequest) (*mediaadminv1.SetItemTagsResponse, error) {
	_, err := s.m.SetItemTags(ctx, &tvmgmtv1.SetItemTagsRequest{
		ItemId: req.GetItemId(), TagIds: req.GetTagIds(),
	})
	if err != nil {
		return nil, err
	}
	return &mediaadminv1.SetItemTagsResponse{}, nil
}

func (s mediaAdminServer) ListCollections(ctx context.Context, req *mediaadminv1.ListCollectionsRequest) (*mediaadminv1.ListCollectionsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "collections are not supported for TV shows")
}

func (s mediaAdminServer) GetCollectionItems(ctx context.Context, req *mediaadminv1.GetCollectionItemsRequest) (*mediaadminv1.GetCollectionItemsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "collections are not supported for TV shows")
}

func (s mediaAdminServer) GetCalendar(ctx context.Context, req *mediaadminv1.GetCalendarRequest) (*mediaadminv1.GetCalendarResponse, error) {
	resp, err := s.m.GetCalendar(ctx, &tvmgmtv1.GetCalendarRequest{
		StartDate: req.GetStartDate(), EndDate: req.GetEndDate(),
		IncludeUnmonitored: req.GetIncludeUnmonitored(),
	})
	if err != nil {
		return nil, err
	}
	items := make([]*mediaadminv1.CalendarItem, 0, len(resp.GetItems()))
	for _, it := range resp.GetItems() {
		items = append(items, &mediaadminv1.CalendarItem{
			Id: it.GetEpisodeId(), ParentId: it.GetSeriesId(),
			Title: it.GetSeriesName(), Subtitle: it.GetEpisodeName(),
			Date: it.GetAirDate(), Monitored: it.GetMonitored(), HasFile: it.GetHasFile(),
			Metadata: map[string]string{
				"tmdb_id":        strconv.Itoa(int(it.GetTmdbId())),
				"season_number":  strconv.Itoa(int(it.GetSeasonNumber())),
				"episode_number": strconv.Itoa(int(it.GetEpisodeNumber())),
			},
		})
	}
	return &mediaadminv1.GetCalendarResponse{Items: items}, nil
}
