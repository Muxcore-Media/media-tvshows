package internal

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
	"github.com/Muxcore-Media/core/sdk/go/module/meshtls"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

const (
	titleSourcePrimary  = "primary"
	titleSourceOriginal = "original"
	titleSourceTMDBAlt  = "tmdb_alt"
	titleSourceUser     = "user"
)

var (
	reSpaces           = regexp.MustCompile(`\s+`)
	reTrailingYear     = regexp.MustCompile(`(?i)\s*[\(\[]((?:19|20)\d{2})[\)\]]\s*$`)
	reTrailingBareYear = regexp.MustCompile(`(?i)\s+((19|20)\d{2})$`)
)

func splitTitleYear(title string, year int32) (string, int32) {
	title = strings.TrimSpace(title)
	if m := reTrailingYear.FindStringSubmatch(title); len(m) >= 2 {
		y, err := strconv.Atoi(m[1])
		if err == nil && y >= 1900 {
			title = strings.TrimSpace(reTrailingYear.ReplaceAllString(title, ""))
			if year == 0 {
				year = int32(y)
			}
			return title, year
		}
	}
	if m := reTrailingBareYear.FindStringSubmatch(title); len(m) >= 2 {
		y, err := strconv.Atoi(m[1])
		if err == nil && y >= 1900 {
			title = strings.TrimSpace(reTrailingBareYear.ReplaceAllString(title, ""))
			if year == 0 {
				year = int32(y)
			}
		}
	}
	return title, year
}

func yearsCompatible(want, have int32) bool {
	if want == 0 || have == 0 {
		return true
	}
	diff := want - have
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1
}

func cleanMatchTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '`' || r == '´':
		default:
			b.WriteByte(' ')
		}
	}
	s = reSpaces.ReplaceAllString(strings.TrimSpace(b.String()), " ")
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, art) {
			s = strings.TrimSpace(s[len(art):])
			break
		}
	}
	return s
}

func (m *Module) ensureSeriesTitlesTable(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS series_titles (
			id TEXT PRIMARY KEY,
			series_id TEXT NOT NULL,
			title TEXT NOT NULL,
			clean_title TEXT NOT NULL,
			source TEXT NOT NULL,
			UNIQUE(series_id, clean_title),
			FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create series_titles: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_series_titles_clean ON series_titles(clean_title)
	`); err != nil {
		return fmt.Errorf("create series_titles index: %w", err)
	}
	return nil
}

func (m *Module) backfillSeriesTitles(ctx context.Context) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT s.id, s.name, s.original_name FROM series s
		WHERE NOT EXISTS (
			SELECT 1 FROM series_titles t WHERE t.series_id = s.id AND t.source IN ('primary','original')
		)
	`)
	if err != nil {
		return
	}
	// Drain and close the cursor before upserting: writing while the read
	// cursor is open risks SQLITE_BUSY and deadlocks on a single-conn pool.
	type pending struct{ id, name, original string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.name, &p.original); err != nil {
			continue
		}
		todo = append(todo, p)
	}
	iterErr := rows.Err()
	_ = rows.Close()
	if iterErr != nil {
		return
	}
	for _, p := range todo {
		m.upsertSeriesTitleLocked(ctx, p.id, p.name, titleSourcePrimary)
		if p.original != "" && cleanMatchTitle(p.original) != cleanMatchTitle(p.name) {
			m.upsertSeriesTitleLocked(ctx, p.id, p.original, titleSourceOriginal)
		}
	}
}

func (m *Module) upsertSeriesTitleLocked(ctx context.Context, seriesID, title, source string) {
	title = strings.TrimSpace(title)
	clean := cleanMatchTitle(title)
	if seriesID == "" || clean == "" {
		return
	}
	id := fmt.Sprintf("st_%s_%s_%d", seriesID, source, time.Now().UnixNano())
	_, _ = m.db.ExecContext(ctx, `
		INSERT INTO series_titles (id, series_id, title, clean_title, source)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(series_id, clean_title) DO UPDATE SET
			title = excluded.title,
			source = CASE
				WHEN series_titles.source = 'user' THEN series_titles.source
				WHEN excluded.source = 'user' THEN 'user'
				WHEN series_titles.source = 'primary' THEN series_titles.source
				WHEN excluded.source = 'primary' THEN 'primary'
				WHEN series_titles.source = 'original' THEN series_titles.source
				ELSE excluded.source
			END
	`, id, seriesID, title, clean, source)
}

func (m *Module) syncSeriesPrimaryTitlesLocked(ctx context.Context, seriesID, name, original string) {
	_, _ = m.db.ExecContext(ctx, `DELETE FROM series_titles WHERE series_id = ? AND source IN (?, ?)`,
		seriesID, titleSourcePrimary, titleSourceOriginal)
	m.upsertSeriesTitleLocked(ctx, seriesID, name, titleSourcePrimary)
	if original != "" && cleanMatchTitle(original) != cleanMatchTitle(name) {
		m.upsertSeriesTitleLocked(ctx, seriesID, original, titleSourceOriginal)
	}
}

func (m *Module) replaceSeriesTMDBAltTitlesLocked(ctx context.Context, seriesID string, titles []string) {
	_, _ = m.db.ExecContext(ctx, `DELETE FROM series_titles WHERE series_id = ? AND source = ?`,
		seriesID, titleSourceTMDBAlt)
	for _, t := range titles {
		m.upsertSeriesTitleLocked(ctx, seriesID, t, titleSourceTMDBAlt)
	}
}

func (m *Module) syncSeriesTitlesFromTMDB(ctx context.Context, seriesID string, tmdbID int32, name, original string) {
	if seriesID == "" {
		return
	}
	m.mu.Lock()
	if m.db != nil {
		m.syncSeriesPrimaryTitlesLocked(ctx, seriesID, name, original)
	}
	m.mu.Unlock()

	if tmdbID == 0 {
		return
	}
	alts, err := m.fetchSeriesAlternativeTitles(ctx, tmdbID)
	if err != nil {
		slog.Debug("fetch series alternative titles", "series_id", seriesID, "error", err)
		return
	}
	m.mu.Lock()
	if m.db != nil {
		m.replaceSeriesTMDBAltTitlesLocked(ctx, seriesID, alts)
	}
	m.mu.Unlock()
}

func (m *Module) fetchSeriesAlternativeTitles(ctx context.Context, tmdbID int32) ([]string, error) {
	metaAddr, err := m.findMetadataModule(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := meshtls.Dial(metaAddr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	resp, err := metadatav1.NewMetadataServiceClient(conn).GetAlternativeTitles(ctx, &metadatav1.GetAlternativeTitlesRequest{
		Id:   tmdbID,
		Type: metadatav1.MediaType_MEDIA_TYPE_TV,
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.GetTitles()))
	for _, t := range resp.GetTitles() {
		if s := strings.TrimSpace(t.GetTitle()); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *Module) ListAlternateTitles(ctx context.Context, req *tvmgmtv1.ListAlternateTitlesRequest) (*tvmgmtv1.ListAlternateTitlesResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetSeriesId() == "" {
		return nil, fmt.Errorf("series_id required")
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, title, clean_title, source FROM series_titles WHERE series_id = ? ORDER BY source, title`,
		req.GetSeriesId(),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var titles []*tvmgmtv1.AlternateTitle
	for rows.Next() {
		var id, title, clean, source string
		if err := rows.Scan(&id, &title, &clean, &source); err != nil {
			continue
		}
		titles = append(titles, &tvmgmtv1.AlternateTitle{
			Id: id, Title: title, CleanTitle: clean, Source: source,
		})
	}
	return &tvmgmtv1.ListAlternateTitlesResponse{Titles: titles}, nil
}

func (m *Module) AddAlternateTitle(ctx context.Context, req *tvmgmtv1.AddAlternateTitleRequest) (*tvmgmtv1.AddAlternateTitleResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetSeriesId() == "" || strings.TrimSpace(req.GetTitle()) == "" {
		return nil, fmt.Errorf("series_id and title required")
	}
	var exists string
	_ = m.db.QueryRowContext(ctx, `SELECT id FROM series WHERE id = ?`, req.GetSeriesId()).Scan(&exists)
	if exists == "" {
		return nil, fmt.Errorf("series not found: %s", req.GetSeriesId())
	}
	title := strings.TrimSpace(req.GetTitle())
	clean := cleanMatchTitle(title)
	id := fmt.Sprintf("st_%s_user_%d", req.GetSeriesId(), time.Now().UnixNano())
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO series_titles (id, series_id, title, clean_title, source)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(series_id, clean_title) DO UPDATE SET
			title = excluded.title,
			source = 'user'
	`, id, req.GetSeriesId(), title, clean, titleSourceUser)
	if err != nil {
		return nil, fmt.Errorf("insert alternate title: %w", err)
	}
	var outID, outTitle, outClean, outSource string
	_ = m.db.QueryRowContext(ctx,
		`SELECT id, title, clean_title, source FROM series_titles WHERE series_id = ? AND clean_title = ?`,
		req.GetSeriesId(), clean,
	).Scan(&outID, &outTitle, &outClean, &outSource)
	return &tvmgmtv1.AddAlternateTitleResponse{
		Title: &tvmgmtv1.AlternateTitle{Id: outID, Title: outTitle, CleanTitle: outClean, Source: outSource},
	}, nil
}

func (m *Module) RemoveAlternateTitle(ctx context.Context, req *tvmgmtv1.RemoveAlternateTitleRequest) (*tvmgmtv1.RemoveAlternateTitleResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if req.GetSeriesId() == "" || req.GetTitleId() == "" {
		return nil, fmt.Errorf("series_id and title_id required")
	}
	res, err := m.db.ExecContext(ctx,
		`DELETE FROM series_titles WHERE series_id = ? AND id = ? AND source = ?`,
		req.GetSeriesId(), req.GetTitleId(), titleSourceUser,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("user alternate title not found")
	}
	return &tvmgmtv1.RemoveAlternateTitleResponse{}, nil
}
