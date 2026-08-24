package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
)

const (
	historyGrab       = "grab"
	historyImport     = "import"
	historyDeleteItem = "delete_item"
	historyDeleteFile = "delete_file"
)

type historyEntry struct {
	EventType   string
	ItemID      string
	Title       string
	SourceTitle string
	Quality     string
	Indexer     string
	FilePath    string
	DownloadID  string
	Data        map[string]any
}

func (m *Module) ensureHistoryTable(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS history (
			id           TEXT PRIMARY KEY,
			event_type   TEXT NOT NULL,
			item_id      TEXT NOT NULL,
			title        TEXT NOT NULL DEFAULT '',
			source_title TEXT NOT NULL DEFAULT '',
			quality      TEXT NOT NULL DEFAULT '',
			indexer      TEXT NOT NULL DEFAULT '',
			file_path    TEXT NOT NULL DEFAULT '',
			download_id  TEXT NOT NULL DEFAULT '',
			data_json    TEXT NOT NULL DEFAULT '{}',
			created_at   TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create history table: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_history_created ON history(created_at DESC)
	`); err != nil {
		return fmt.Errorf("create history created index: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_history_item ON history(item_id, created_at DESC)
	`); err != nil {
		return fmt.Errorf("create history item index: %w", err)
	}
	return nil
}

func (m *Module) appendHistory(ctx context.Context, e historyEntry) {
	if m.db == nil || e.ItemID == "" || e.EventType == "" {
		return
	}
	dataJSON := "{}"
	if e.Data != nil {
		if b, err := json.Marshal(e.Data); err == nil {
			dataJSON = string(b)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("th_%d", time.Now().UnixNano())
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO history (id, event_type, item_id, title, source_title, quality, indexer, file_path, download_id, data_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, e.EventType, e.ItemID, e.Title, e.SourceTitle, e.Quality, e.Indexer, e.FilePath, e.DownloadID, dataJSON, now,
	)
	if err != nil {
		slog.Warn("append history failed", "event_type", e.EventType, "item_id", e.ItemID, "error", err)
	}
}

func (m *Module) subscribeToDownloadDispatched() {
	time.Sleep(15 * time.Second)
	if m.mc == nil {
		return
	}
	ch, cancel, err := m.mc.Events.Subscribe(context.Background(), contracts.EventDownloadDispatched)
	if err != nil {
		slog.Warn("subscribe to download dispatched events", "error", err)
		return
	}
	go func() {
		for evt := range ch {
			var p contracts.DownloadDispatchedPayload
			if err := json.Unmarshal(evt.Payload, &p); err != nil {
				continue
			}
			m.handleDownloadDispatched(context.Background(), p)
		}
		cancel()
	}()
	slog.Info("subscribed to download dispatched events")
}

func (m *Module) resolveSeriesIDForGrab(ctx context.Context, db *sql.DB, p contracts.DownloadDispatchedPayload) (seriesID, title string) {
	if p.SeriesID != "" {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM series WHERE id = ?`, p.SeriesID).Scan(&name)
		if err == nil {
			return p.SeriesID, name
		}
	}
	itemID := p.ItemID
	if itemID == "" {
		return "", ""
	}
	if idx := strings.Index(itemID, ":S"); idx > 0 && strings.HasSuffix(itemID, ":pack") {
		candidate := itemID[:idx]
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM series WHERE id = ?`, candidate).Scan(&name)
		if err == nil {
			return candidate, name
		}
	}
	var sid, name string
	err := db.QueryRowContext(ctx,
		`SELECT e.series_id, s.name FROM episodes e INNER JOIN series s ON s.id = e.series_id WHERE e.id = ?`,
		itemID,
	).Scan(&sid, &name)
	if err == nil {
		return sid, name
	}
	return "", ""
}

func (m *Module) handleDownloadDispatched(ctx context.Context, p contracts.DownloadDispatchedPayload) {
	itemType := strings.ToLower(strings.TrimSpace(p.ItemType))
	if itemType != "" && itemType != "tv" {
		return
	}
	if p.ItemID == "" && p.SeriesID == "" {
		return
	}

	db := m.dbConn()
	if db == nil {
		return
	}

	seriesID, title := m.resolveSeriesIDForGrab(ctx, db, p)
	if seriesID == "" {
		return
	}
	if title == "" {
		title = p.Title
	}

	m.appendHistory(ctx, historyEntry{
		EventType:   historyGrab,
		ItemID:      seriesID,
		Title:       title,
		SourceTitle: p.Title,
		Indexer:     p.Indexer,
		DownloadID:  p.DownloadID,
		Data: map[string]any{
			"guid":              p.GUID,
			"score":             p.Score,
			"size":              p.Size,
			"download_protocol": p.DownloadProtocol,
			"tmdb_id":           p.TMDBID,
			"wanted_item_id":    p.ItemID,
		},
	})
}

func (m *Module) ListHistory(ctx context.Context, req *mediaadminv1.ListHistoryRequest) (*mediaadminv1.ListHistoryResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	where := []string{"1=1"}
	args := []any{}
	if req.GetItemId() != "" {
		where = append(where, "item_id = ?")
		args = append(args, req.GetItemId())
	}
	if req.GetEventType() != "" {
		where = append(where, "event_type = ?")
		args = append(args, req.GetEventType())
	}
	clause := strings.Join(where, " AND ")

	var total int32
	countArgs := append([]any{}, args...)
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history WHERE `+clause, countArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count history: %w", err)
	}

	queryArgs := append(args, pageSize, offset)
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, event_type, item_id, title, source_title, quality, indexer, file_path, download_id, created_at
		 FROM history WHERE `+clause+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []*mediaadminv1.HistoryRecord
	for rows.Next() {
		var r mediaadminv1.HistoryRecord
		if err := rows.Scan(
			&r.Id, &r.EventType, &r.ItemId, &r.Title, &r.SourceTitle,
			&r.Quality, &r.Indexer, &r.FilePath, &r.DownloadId, &r.CreatedAt,
		); err != nil {
			continue
		}
		records = append(records, &r)
	}

	return &mediaadminv1.ListHistoryResponse{
		Records:  records,
		Total:    total,
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}
