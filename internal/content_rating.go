package internal

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Parental classification (ADR-0031 Decision 2, roadmap T-M4-01 slice S3).
//
// media-tvshows is the authority for a series' classification (ADR-0009).
// Seasons and episodes carry none of their own: they inherit the series', and
// GetEpisode exists so a caller holding only an episode id can find the series.
// A series is in exactly one of three states:
//
//   - rated:       a ladder token recorded by a trusted source;
//   - unrated:     an explicit "NR" recorded by a trusted source;
//   - unavailable: nothing recorded (the state of every pre-existing row).
//
// A rating is never inferred from other data: vote_average is not a rating, and
// nothing in a metadata payload, AddTVShow, UpdateMetadata or a refresh writes
// the rating columns. Only the operator source exists today; a "tmdb" source is
// a later slice.
//
// Authorization is NOT checked in this module. SetContentRating, like
// SetItemTags, trusts its caller: the consumer BFF restricts both to admin or
// manager principals before calling, and the mesh transport authenticates the
// caller as a module.

const (
	// contentRatingSourceOperator marks a value written by SetContentRating.
	contentRatingSourceOperator = "operator"
	// contentRatingNR is the stored and exposed token for an explicit unrated
	// record. It is a state, not a rating, so it is not on the ladder.
	contentRatingNR = "NR"
)

// ratingLevels is a local copy of the ADR-0031 rating ladder. It must stay
// identical to parental.RatingLevel in userdata-local (package parental,
// evaluate.go); it is copied rather than imported so media-tvshows takes no new
// cross-module dependency. TestRatingLadderPinned pins the token list.
var ratingLevels = map[string]int{
	"G": 0, "TV-Y": 0, "TV-Y7": 0, "TV-Y7-FV": 0, "ALL": 0, "E": 0,
	"PG": 1, "TV-G": 1, "TV-PG": 1, "E10+": 1,
	"PG-13": 2, "TV-14": 2, "T": 2,
	"R": 3, "TV-MA": 3, "M": 3, "MA": 3,
	"NC-17": 4, "AO": 4, "X": 4,
}

// ratingLevel returns the ladder position of a token, ignoring case and
// surrounding space. ok is false for anything not on the ladder, including NR
// and the empty string.
func ratingLevel(token string) (level int, ok bool) {
	level, ok = ratingLevels[strings.ToUpper(strings.TrimSpace(token))]
	return level, ok
}

// normalizeTag is the single tag comparison form (trimmed, case-folded), the
// same as parental.normalizeTag, applied to both sides of an exact match.
func normalizeTag(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

// seriesSelectCols is the column list scanSeries and scanSingleSeries expect.
const seriesSelectCols = `id, tmdb_id, name, original_name, year, overview, tagline,
		status, network, first_air_date, last_air_date, vote_average, genres,
		poster_path, backdrop_path, monitored, total_seasons, total_episodes,
		quality_profile_id, root_folder_path, series_type, created_at, updated_at,
		parental_rating, parental_rating_source`

// trustedRating returns the stored rating and source when they are a value this
// module would have written (source "operator", and "NR" or a ladder token).
// Anything else, including a hand-edited row, reads as unavailable so a bad
// value can never widen access.
func trustedRating(rating, source string) (string, string) {
	rating = strings.ToUpper(strings.TrimSpace(rating))
	if source != contentRatingSourceOperator {
		return "", ""
	}
	if _, onLadder := ratingLevel(rating); onLadder || rating == contentRatingNR {
		return rating, source
	}
	return "", ""
}

// SetContentRating records, replaces or clears the operator classification of a
// series. It stores source "operator". A ladder token records a rating;
// explicit_unrated records an explicit NR; an empty rating without
// explicit_unrated clears the operator value, returning the series to
// "unavailable". Unknown tokens are rejected with InvalidArgument and an
// unknown series with NotFound.
//
// No admin/role check happens here (see the comment at the top of this file).
func (m *Module) SetContentRating(ctx context.Context, req *tvmgmtv1.SetContentRatingRequest) (*tvmgmtv1.SetContentRatingResponse, error) {
	seriesID := req.GetSeriesId()
	if seriesID == "" {
		return nil, status.Error(codes.InvalidArgument, "series_id required")
	}
	token := strings.ToUpper(strings.TrimSpace(req.GetContentRating()))
	switch {
	case req.GetExplicitUnrated() && token != "":
		return nil, status.Error(codes.InvalidArgument, "content_rating must be empty when explicit_unrated is set")
	case token != "":
		if _, ok := ratingLevel(token); !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unsupported content_rating %q", req.GetContentRating())
		}
	}

	rating, source := token, contentRatingSourceOperator
	switch {
	case req.GetExplicitUnrated():
		rating = contentRatingNR
	case token == "":
		rating, source = "", ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	res, err := m.db.ExecContext(ctx,
		`UPDATE series SET parental_rating = ?, parental_rating_source = ? WHERE id = ?`,
		rating, source, seriesID)
	if err != nil {
		return nil, fmt.Errorf("set content rating: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Errorf(codes.NotFound, "series not found: %s", seriesID)
	}
	return &tvmgmtv1.SetContentRatingResponse{}, nil
}

// GetEpisode returns one episode with its owning series_id. It is the lookup
// the BFF uses to map /stream/tv/<episode_id> to a series for classification.
// An unknown id, and an episode whose series no longer exists, are NotFound.
func (m *Module) GetEpisode(ctx context.Context, req *tvmgmtv1.GetEpisodeRequest) (*tvmgmtv1.GetEpisodeResponse, error) {
	db := m.dbConn()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	id := strings.TrimSpace(req.GetEpisodeId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "episode_id required")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT e.id, e.series_id, e.season_id, e.tmdb_id, e.episode_number, e.season_number,
		 e.absolute_number, e.name, e.overview, e.air_date, e.still_path, e.monitored, e.has_file,
		 e.created_at, e.updated_at
		 FROM episodes e INNER JOIN series s ON s.id = e.series_id
		 WHERE e.id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get episode: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get episode: %w", err)
		}
		return nil, status.Errorf(codes.NotFound, "episode not found: %s", id)
	}
	ep := m.scanEpisode(rows)
	if ep == nil {
		return nil, fmt.Errorf("get episode: scan failed for %s", id)
	}
	return &tvmgmtv1.GetEpisodeResponse{Episode: ep}, nil
}

// loadTagLabels returns the sorted operator tag labels of every id, in one
// query per chunk of ids. Callers must not have a result cursor open on db.
// An error is returned, never swallowed: a series must not leave this module
// looking untagged because a query failed.
func loadTagLabels(ctx context.Context, db *sql.DB, ids []string) (map[string][]string, error) {
	out := make(map[string][]string, len(ids))
	const chunk = 400
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx,
			`SELECT it.item_id, t.label FROM item_tags it
			 INNER JOIN tags t ON t.id = it.tag_id
			 WHERE it.item_id IN (`+ph+`) ORDER BY t.label`, args...)
		if err != nil {
			return nil, fmt.Errorf("load item tags: %w", err)
		}
		for rows.Next() {
			var id, label string
			if err := rows.Scan(&id, &label); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan item tag: %w", err)
			}
			out[id] = append(out[id], label)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("load item tags: %w", err)
		}
	}
	return out, nil
}

// attachTagLabels fills tag_labels on every series.
func attachTagLabels(ctx context.Context, db *sql.DB, series ...*tvmgmtv1.TVSeries) error {
	if len(series) == 0 {
		return nil
	}
	ids := make([]string, 0, len(series))
	for _, s := range series {
		ids = append(ids, s.GetId())
	}
	labels, err := loadTagLabels(ctx, db, ids)
	if err != nil {
		return err
	}
	for _, s := range series {
		s.TagLabels = labels[s.GetId()]
	}
	return nil
}

// classificationFilter is a validated ClassificationFilter.
type classificationFilter struct {
	maxLevel     int
	hasMax       bool
	allowUnrated bool
	blocked      []string
	allowed      []string
}

// compileClassificationFilter validates f. It returns nil (no narrowing) when f
// is absent or not enabled. Anything it cannot interpret is an error, never a
// silently weaker filter.
func compileClassificationFilter(f *tvmgmtv1.ClassificationFilter) (*classificationFilter, error) {
	if f == nil || !f.GetEnabled() {
		return nil, nil
	}
	cf := &classificationFilter{allowUnrated: f.GetAllowUnrated()}
	if ceiling := strings.TrimSpace(f.GetMaxRating()); ceiling != "" {
		level, ok := ratingLevel(ceiling)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unsupported max_rating %q", f.GetMaxRating())
		}
		cf.maxLevel, cf.hasMax = level, true
	}
	norm := func(field string, in []string) ([]string, error) {
		out := make([]string, 0, len(in))
		for _, tag := range in {
			tag = normalizeTag(tag)
			if tag == "" {
				return nil, status.Errorf(codes.InvalidArgument, "%s contains an empty tag", field)
			}
			out = append(out, tag)
		}
		return out, nil
	}
	var err error
	if cf.blocked, err = norm("blocked_tags", f.GetBlockedTags()); err != nil {
		return nil, err
	}
	if cf.allowed, err = norm("allowed_tags", f.GetAllowedTags()); err != nil {
		return nil, err
	}
	return cf, nil
}

// visible reports whether a series with the given trusted rating and tag
// labels passes the filter. It mirrors parental.Evaluate for a restricted
// policy: the rating is checked first (unavailable is always denied), then
// tags, so a tag match never overrides a rating denial.
func (cf *classificationFilter) visible(rating string, tagLabels []string) bool {
	switch {
	case rating == contentRatingNR:
		if !cf.allowUnrated {
			return false
		}
	case rating != "":
		level, ok := ratingLevel(rating)
		if !ok || (cf.hasMax && level > cf.maxLevel) {
			return false
		}
	default: // unavailable
		return false
	}
	if len(cf.blocked) == 0 && len(cf.allowed) == 0 {
		return true
	}
	tags := make([]string, 0, len(tagLabels))
	for _, tag := range tagLabels {
		if tag = normalizeTag(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	for _, b := range cf.blocked {
		if slices.Contains(tags, b) {
			return false
		}
	}
	if len(cf.allowed) > 0 && !slices.ContainsFunc(cf.allowed, func(a string) bool { return slices.Contains(tags, a) }) {
		return false
	}
	return true
}

// listVisibleSeries serves ListTVShows when a classification filter is enabled.
// The filter cannot be expressed in SQL without diverging from Go's Unicode
// case folding (SQLite lower() folds ASCII only, which would let a non-ASCII
// blocked tag through), so it selects the ordered ids matching the ordinary
// criteria, evaluates visibility in Go, counts and paginates the visible ids,
// then loads the full rows for the requested page.
func (m *Module) listVisibleSeries(ctx context.Context, db *sql.DB, cf *classificationFilter, where string, args []any, orderBy string, page, pageSize int) ([]*tvmgmtv1.TVSeries, int, error) {
	type candidate struct{ id, rating string }
	rows, err := db.QueryContext(ctx,
		`SELECT id, parental_rating, parental_rating_source FROM series`+where+` ORDER BY `+orderBy, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query series: %w", err)
	}
	var cands []candidate
	for rows.Next() {
		var id, rating, source string
		if err := rows.Scan(&id, &rating, &source); err != nil {
			_ = rows.Close()
			return nil, 0, fmt.Errorf("scan series: %w", err)
		}
		rating, _ = trustedRating(rating, source)
		cands = append(cands, candidate{id: id, rating: rating})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, 0, fmt.Errorf("query series: %w", err)
	}

	// Series with no usable rating are hidden whatever the tags say, so only
	// the rest need their tags loaded.
	rated := make([]string, 0, len(cands))
	for _, c := range cands {
		if c.rating != "" {
			rated = append(rated, c.id)
		}
	}
	labels, err := loadTagLabels(ctx, db, rated)
	if err != nil {
		return nil, 0, err
	}
	var visible []string
	for _, c := range cands {
		if cf.visible(c.rating, labels[c.id]) {
			visible = append(visible, c.id)
		}
	}

	total := len(visible)
	start := min((page-1)*pageSize, total)
	pageIDs := visible[start:min(start+pageSize, total)]
	if len(pageIDs) == 0 {
		return nil, total, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(pageIDs)), ",")
	pageArgs := make([]any, len(pageIDs))
	for i, id := range pageIDs {
		pageArgs[i] = id
	}
	prows, err := db.QueryContext(ctx, `SELECT `+seriesSelectCols+` FROM series WHERE id IN (`+ph+`)`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query series page: %w", err)
	}
	byID := make(map[string]*tvmgmtv1.TVSeries, len(pageIDs))
	for prows.Next() {
		if s := m.scanSeries(prows); s != nil {
			byID[s.GetId()] = s
		}
	}
	err = prows.Err()
	_ = prows.Close()
	if err != nil {
		return nil, 0, fmt.Errorf("query series page: %w", err)
	}
	// Keep the order the ids were sorted in. A series deleted between the two
	// queries simply drops out of the page.
	out := make([]*tvmgmtv1.TVSeries, 0, len(pageIDs))
	for _, id := range pageIDs {
		if s, ok := byID[id]; ok {
			out = append(out, s)
		}
	}

	// Writers (SetContentRating, SetItemTags) can commit between the queries
	// above. Re-evaluate every returned series against the rating and tags it is
	// actually returned with, so a page can only ever hold series that pass the
	// filter as they are reported. A series changed in that window is dropped
	// (total may then be one high, which is the safe direction).
	if err := attachTagLabels(ctx, db, out...); err != nil {
		return nil, 0, err
	}
	kept := out[:0]
	for _, s := range out {
		if cf.visible(s.GetContentRating(), s.GetTagLabels()) {
			kept = append(kept, s)
		}
	}
	return kept, total, nil
}
