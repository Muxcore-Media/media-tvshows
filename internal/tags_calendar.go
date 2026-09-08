package internal

import (
	"context"
	"fmt"
	"strings"
	"time"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func (m *Module) CreateTag(ctx context.Context, req *tvmgmtv1.CreateTagRequest) (*tvmgmtv1.CreateTagResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	label := strings.TrimSpace(req.GetLabel())
	if label == "" {
		return nil, fmt.Errorf("label required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("tag_%d", time.Now().UnixNano())
	_, err := m.db.ExecContext(ctx, `INSERT INTO tags (id, label, created_at) VALUES (?, ?, ?)`, id, label, now)
	if err != nil {
		return nil, fmt.Errorf("create tag: %w", err)
	}
	return &tvmgmtv1.CreateTagResponse{TagId: id}, nil
}

func (m *Module) DeleteTag(ctx context.Context, req *tvmgmtv1.DeleteTagRequest) (*tvmgmtv1.DeleteTagResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetTagId() == "" {
		return nil, fmt.Errorf("tag_id required")
	}
	_, _ = m.db.ExecContext(ctx, `DELETE FROM item_tags WHERE tag_id = ?`, req.GetTagId())
	_, err := m.db.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, req.GetTagId())
	if err != nil {
		return nil, fmt.Errorf("delete tag: %w", err)
	}
	return &tvmgmtv1.DeleteTagResponse{}, nil
}

func (m *Module) ListTags(ctx context.Context, req *tvmgmtv1.ListTagsRequest) (*tvmgmtv1.ListTagsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id, label, created_at FROM tags ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tags []*tvmgmtv1.Tag
	for rows.Next() {
		var id, label, created string
		if err := rows.Scan(&id, &label, &created); err != nil {
			return nil, err
		}
		tags = append(tags, &tvmgmtv1.Tag{Id: id, Label: label, CreatedAt: created})
	}
	return &tvmgmtv1.ListTagsResponse{Tags: tags}, nil
}

func (m *Module) SetItemTags(ctx context.Context, req *tvmgmtv1.SetItemTagsRequest) (*tvmgmtv1.SetItemTagsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetItemId() == "" {
		return nil, fmt.Errorf("item_id required")
	}
	var exists string
	if err := m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE id = ?`, req.GetItemId()).Scan(&exists); err != nil || exists == "" {
		return nil, fmt.Errorf("series not found: %s", req.GetItemId())
	}
	_, _ = m.db.ExecContext(ctx, `DELETE FROM item_tags WHERE item_id = ?`, req.GetItemId())
	for _, tagID := range req.GetTagIds() {
		if tagID == "" {
			continue
		}
		_, err := m.db.ExecContext(ctx, `INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)`, req.GetItemId(), tagID)
		if err != nil {
			return nil, fmt.Errorf("set tag: %w", err)
		}
	}
	return &tvmgmtv1.SetItemTagsResponse{}, nil
}

func (m *Module) GetItemTags(ctx context.Context, req *tvmgmtv1.GetItemTagsRequest) (*tvmgmtv1.GetItemTagsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetItemId() == "" {
		return nil, fmt.Errorf("item_id required")
	}
	var exists string
	if err := m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE id = ?`, req.GetItemId()).Scan(&exists); err != nil || exists == "" {
		return nil, fmt.Errorf("series not found: %s", req.GetItemId())
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT t.id, t.label, t.created_at FROM tags t
		 INNER JOIN item_tags it ON it.tag_id = t.id
		 WHERE it.item_id = ? ORDER BY t.label`, req.GetItemId())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tags []*tvmgmtv1.Tag
	for rows.Next() {
		var id, label, created string
		if err := rows.Scan(&id, &label, &created); err != nil {
			return nil, err
		}
		tags = append(tags, &tvmgmtv1.Tag{Id: id, Label: label, CreatedAt: created})
	}
	return &tvmgmtv1.GetItemTagsResponse{Tags: tags}, nil
}

func (m *Module) GetCalendar(ctx context.Context, req *tvmgmtv1.GetCalendarRequest) (*tvmgmtv1.GetCalendarResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	start := req.GetStartDate()
	end := req.GetEndDate()
	if start == "" || end == "" {
		return nil, fmt.Errorf("start_date and end_date required (YYYY-MM-DD)")
	}

	query := `SELECT e.id, e.series_id, s.name, s.tmdb_id, e.season_number, e.episode_number,
		e.name, e.air_date, e.monitored, e.has_file
		FROM episodes e
		INNER JOIN series s ON s.id = e.series_id
		WHERE e.air_date >= ? AND e.air_date <= ?`
	args := []any{start, end}
	if !req.GetIncludeUnmonitored() {
		query += ` AND e.monitored = 1 AND s.monitored = 1`
	}
	query += ` ORDER BY e.air_date, s.name, e.season_number, e.episode_number`

	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []*tvmgmtv1.CalendarItem
	for rows.Next() {
		var epID, seriesID, seriesName, epName, airDate string
		var tmdbID, season, episode int32
		var monitored, hasFile int
		if err := rows.Scan(&epID, &seriesID, &seriesName, &tmdbID, &season, &episode, &epName, &airDate, &monitored, &hasFile); err != nil {
			return nil, err
		}
		items = append(items, &tvmgmtv1.CalendarItem{
			EpisodeId:     epID,
			SeriesId:      seriesID,
			SeriesName:    seriesName,
			TmdbId:        tmdbID,
			SeasonNumber:  season,
			EpisodeNumber: episode,
			EpisodeName:   epName,
			AirDate:       airDate,
			Monitored:     monitored == 1,
			HasFile:       hasFile == 1,
		})
	}
	return &tvmgmtv1.GetCalendarResponse{Items: items}, nil
}
