package internal

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRatingLadderPinned pins the local copy of the ADR-0031 ladder. It must
// stay identical to parental.RatingLevel in userdata-local; a change on either
// side has to be made on both, so this test fails until the copy is updated on
// purpose.
func TestRatingLadderPinned(t *testing.T) {
	want := map[string]int{
		"G": 0, "TV-Y": 0, "TV-Y7": 0, "TV-Y7-FV": 0, "ALL": 0, "E": 0,
		"PG": 1, "TV-G": 1, "TV-PG": 1, "E10+": 1,
		"PG-13": 2, "TV-14": 2, "T": 2,
		"R": 3, "TV-MA": 3, "M": 3, "MA": 3,
		"NC-17": 4, "AO": 4, "X": 4,
	}
	if len(ratingLevels) != len(want) {
		t.Errorf("ladder has %d tokens, want %d", len(ratingLevels), len(want))
	}
	for tok, lvl := range want {
		if got, ok := ratingLevel(tok); !ok || got != lvl {
			t.Errorf("ratingLevel(%q) = %d, %v; want %d", tok, got, ok, lvl)
		}
	}
	for _, tok := range []string{"", "NR", "UR", "15", "12A", "PG13", "TV-13", "NC17"} {
		if _, ok := ratingLevel(tok); ok {
			t.Errorf("ratingLevel(%q) accepted; not on the ladder", tok)
		}
	}
	if lvl, ok := ratingLevel("  tv-ma "); !ok || lvl != 3 {
		t.Errorf("ratingLevel is not case/space-insensitive: %d, %v", lvl, ok)
	}
}

var tmdbSeq int32 = 1000

func addSeries(t *testing.T, m *Module, name string) string {
	t.Helper()
	tmdbSeq++
	r, err := m.AddTVShow(context.Background(), &tvmgmtv1.AddTVShowRequest{TmdbId: tmdbSeq, Name: name, Year: 2020})
	if err != nil {
		t.Fatalf("AddTVShow %s: %v", name, err)
	}
	return r.GetSeriesId()
}

func setRating(t *testing.T, m *Module, id, rating string, unrated bool) {
	t.Helper()
	if _, err := m.SetContentRating(context.Background(), &tvmgmtv1.SetContentRatingRequest{
		SeriesId: id, ContentRating: rating, ExplicitUnrated: unrated,
	}); err != nil {
		t.Fatalf("SetContentRating(%s, %q, %v): %v", id, rating, unrated, err)
	}
}

func tagSeries(t *testing.T, m *Module, id string, labels ...string) {
	t.Helper()
	ctx := context.Background()
	existing, err := m.ListTags(ctx, &tvmgmtv1.ListTagsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	byLabel := map[string]string{}
	for _, tg := range existing.GetTags() {
		byLabel[tg.GetLabel()] = tg.GetId()
	}
	var ids []string
	for _, l := range labels {
		tid, ok := byLabel[l]
		if !ok {
			c, err := m.CreateTag(ctx, &tvmgmtv1.CreateTagRequest{Label: l})
			if err != nil {
				t.Fatalf("CreateTag %q: %v", l, err)
			}
			tid = c.GetTagId()
		}
		ids = append(ids, tid)
	}
	if _, err := m.SetItemTags(ctx, &tvmgmtv1.SetItemTagsRequest{ItemId: id, TagIds: ids}); err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}
}

func getSeries(t *testing.T, m *Module, id string) *tvmgmtv1.TVSeries {
	t.Helper()
	r, err := m.GetTVShow(context.Background(), &tvmgmtv1.GetTVShowRequest{SeriesId: id})
	if err != nil {
		t.Fatalf("GetTVShow %s: %v", id, err)
	}
	return r.GetSeries()
}

func requireClass(t *testing.T, s *tvmgmtv1.TVSeries, rating, source string) {
	t.Helper()
	if s.GetContentRating() != rating || s.GetContentRatingSource() != source {
		t.Errorf("%s: rating=%q source=%q, want %q/%q", s.GetName(), s.GetContentRating(), s.GetContentRatingSource(), rating, source)
	}
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %v (%v), want %v", got, err, want)
	}
}

func TestSetContentRatingRoundTrip(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	id := addSeries(t, m, "Round Trip")

	// A new series is unavailable, in every read path.
	requireClass(t, getSeries(t, m, id), "", "")

	setRating(t, m, id, "tv-ma", false) // case-insensitive, stored upper-case
	requireClass(t, getSeries(t, m, id), "TV-MA", "operator")

	list, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetSeries()) != 1 {
		t.Fatalf("list = %d series", len(list.GetSeries()))
	}
	requireClass(t, list.GetSeries()[0], "TV-MA", "operator")

	// Replace.
	setRating(t, m, id, " PG-13 ", false)
	requireClass(t, getSeries(t, m, id), "PG-13", "operator")

	// Unrelated updates keep the classification.
	if _, err := m.UpdateTVShow(ctx, &tvmgmtv1.UpdateTVShowRequest{SeriesId: id, SeriesType: ptr("anime")}); err != nil {
		t.Fatal(err)
	}
	up, err := m.UpdateTVShow(ctx, &tvmgmtv1.UpdateTVShowRequest{SeriesId: id, Monitored: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	requireClass(t, up.GetSeries(), "PG-13", "operator")

	// Explicit NR is a recorded state, distinct from unavailable.
	setRating(t, m, id, "", true)
	requireClass(t, getSeries(t, m, id), "NR", "operator")

	// Empty rating without explicit_unrated clears back to unavailable.
	setRating(t, m, id, "", false)
	requireClass(t, getSeries(t, m, id), "", "")

	// Clearing an already-unavailable series is a no-op, not an error.
	setRating(t, m, id, "", false)
	requireClass(t, getSeries(t, m, id), "", "")
}

func ptr[T any](v T) *T { return &v }

func TestSetContentRatingValidation(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	id := addSeries(t, m, "Validation")
	setRating(t, m, id, "TV-14", false)

	for _, tok := range []string{"15", "12A", "UR", "NR", "PG13", "TV-13", "bogus", "R; DROP TABLE series"} {
		_, err := m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: id, ContentRating: tok})
		requireCode(t, err, codes.InvalidArgument)
	}
	// Contradictory request: a rating together with explicit_unrated.
	_, err := m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: id, ContentRating: "G", ExplicitUnrated: true})
	requireCode(t, err, codes.InvalidArgument)
	_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{ContentRating: "G"})
	requireCode(t, err, codes.InvalidArgument)
	_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: "nope", ContentRating: "G"})
	requireCode(t, err, codes.NotFound)
	_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: "nope", ExplicitUnrated: true})
	requireCode(t, err, codes.NotFound)
	_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: "nope"})
	requireCode(t, err, codes.NotFound)

	// Every rejected write left the stored value untouched.
	requireClass(t, getSeries(t, m, id), "TV-14", "operator")

	// Every ladder token is accepted.
	for tok := range ratingLevels {
		setRating(t, m, id, tok, false)
		requireClass(t, getSeries(t, m, id), tok, "operator")
	}
}

func TestTagLabelsPopulated(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	a := addSeries(t, m, "Tagged A")
	b := addSeries(t, m, "Untagged B")
	tagSeries(t, m, a, "zeta", "Alpha", "mid")

	want := []string{"Alpha", "mid", "zeta"} // sorted by label (SQLite BINARY collation)
	if got := getSeries(t, m, a).GetTagLabels(); !slices.Equal(got, want) {
		t.Errorf("detail tag_labels = %v, want %v", got, want)
	}
	list, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list.GetSeries() {
		switch s.GetId() {
		case a:
			if !slices.Equal(s.GetTagLabels(), want) {
				t.Errorf("list tag_labels(a) = %v, want %v", s.GetTagLabels(), want)
			}
		case b:
			if len(s.GetTagLabels()) != 0 {
				t.Errorf("list tag_labels(b) = %v, want none", s.GetTagLabels())
			}
		}
	}
	up, err := m.UpdateTVShow(ctx, &tvmgmtv1.UpdateTVShowRequest{SeriesId: a, Monitored: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(up.GetSeries().GetTagLabels(), want) {
		t.Errorf("update tag_labels = %v, want %v", up.GetSeries().GetTagLabels(), want)
	}
	// Replacing the tags is reflected immediately.
	tagSeries(t, m, a, "solo")
	if got := getSeries(t, m, a).GetTagLabels(); !slices.Equal(got, []string{"solo"}) {
		t.Errorf("after retag: %v", got)
	}
}

func insertEpisode(t *testing.T, m *Module, seriesID, seasonID, epID string, num int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := m.dbConn().ExecContext(ctx,
		`INSERT OR IGNORE INTO seasons (id, series_id, season_number, name, created_at, updated_at) VALUES (?, ?, 1, 'Season 1', ?, ?)`,
		seasonID, seriesID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.dbConn().ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, name, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		epID, seriesID, seasonID, num, fmt.Sprintf("Episode %d", num), now, now); err != nil {
		t.Fatal(err)
	}
}

func TestGetEpisode(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	s1 := addSeries(t, m, "Show One")
	s2 := addSeries(t, m, "Show Two")
	insertEpisode(t, m, s1, "sea-1", "ep-1", 1)
	insertEpisode(t, m, s1, "sea-1", "ep-2", 2)
	insertEpisode(t, m, s2, "sea-2", "ep-3", 1)

	for ep, series := range map[string]string{"ep-1": s1, "ep-2": s1, "ep-3": s2} {
		r, err := m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: ep})
		if err != nil {
			t.Fatalf("GetEpisode(%s): %v", ep, err)
		}
		e := r.GetEpisode()
		if e.GetId() != ep || e.GetSeriesId() != series || e.GetSeasonId() == "" || e.GetName() == "" {
			t.Errorf("GetEpisode(%s) = %+v, want series %s", ep, e, series)
		}
	}
	// Surrounding whitespace in the id is tolerated (stream paths).
	if r, err := m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: " ep-3 "}); err != nil || r.GetEpisode().GetSeriesId() != s2 {
		t.Errorf("GetEpisode(trimmed) = %v, %v", r, err)
	}

	_, err := m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: "no-such-episode"})
	requireCode(t, err, codes.NotFound)
	_, err = m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: s1}) // a series id is not an episode id
	requireCode(t, err, codes.NotFound)
	_, err = m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{})
	requireCode(t, err, codes.InvalidArgument)

	// An episode whose series is gone has no owning series to classify by.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := m.dbConn().ExecContext(ctx,
		`INSERT INTO episodes (id, series_id, season_id, episode_number, season_number, created_at, updated_at) VALUES ('ep-orphan', 'gone', 'sea-x', 1, 1, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	_, err = m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: "ep-orphan"})
	requireCode(t, err, codes.NotFound)

	// Removing the series makes its episodes unknown too.
	if _, err := m.RemoveTVShow(ctx, &tvmgmtv1.RemoveTVShowRequest{SeriesId: s2}); err != nil {
		t.Fatal(err)
	}
	_, err = m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: "ep-3"})
	requireCode(t, err, codes.NotFound)
}

// TestRatingNeverInferredOrWrittenByOtherPaths covers the trust boundary: only
// SetContentRating produces a classification.
func TestRatingNeverInferredOrWrittenByOtherPaths(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	id := addSeries(t, m, "Not Inferred")

	// vote_average is not a rating.
	if _, err := m.dbConn().ExecContext(ctx, `UPDATE series SET vote_average = 9.9, genres = '["Kids"]' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	// A "content_rating" key in the admin metadata map is ignored.
	if _, err := m.UpdateMetadata(ctx, &mediaadminv1.UpdateMetadataRequest{
		Id: id, Title: "Not Inferred", Metadata: map[string]string{"content_rating": "TV-Y", "parental_rating": "G"},
	}); err != nil {
		t.Fatal(err)
	}
	requireClass(t, getSeries(t, m, id), "", "")
	for _, tag := range []string{"kids", "tv-y", "G"} {
		tagSeries(t, m, id, tag) // tags never imply a rating either
		requireClass(t, getSeries(t, m, id), "", "")
	}

	// Hand-edited or foreign rows never count: wrong source, unknown token,
	// and a "tmdb" source in the operator columns (the tmdb value lives in
	// parental_rating_tmdb) all read as unavailable.
	for _, tc := range []struct{ rating, source string }{
		{"TV-MA", ""}, {"TV-MA", "tmdb"}, {"TV-MA", "other"}, {"15", "operator"}, {"UR", "operator"}, {"", "operator"},
	} {
		if _, err := m.dbConn().ExecContext(ctx,
			`UPDATE series SET parental_rating = ?, parental_rating_source = ? WHERE id = ?`, tc.rating, tc.source, id); err != nil {
			t.Fatal(err)
		}
		requireClass(t, getSeries(t, m, id), "", "")
		l, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{
			ClassificationFilter: &tvmgmtv1.ClassificationFilter{Enabled: true, AllowUnrated: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if l.GetTotal() != 0 || len(l.GetSeries()) != 0 {
			t.Errorf("row %+v visible through the filter (total=%d)", tc, l.GetTotal())
		}
	}
}

// TestLegacyContentRatingColumnIgnored simulates a database written by the
// unmerged v0.1.21 tag, which stored an unauthenticated value in a column
// named content_rating. It must open, and that value must read as unavailable.
func TestLegacyContentRatingColumnIgnored(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	m := openUpgradeModule(t, path)
	id := addSeries(t, m, "Legacy")
	if _, err := m.dbConn().ExecContext(ctx, `ALTER TABLE series ADD COLUMN content_rating TEXT DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.dbConn().ExecContext(ctx, `UPDATE series SET content_rating = 'TV-Y' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	closeUpgradeModule(t, m)

	m = openUpgradeModule(t, path)
	defer closeUpgradeModule(t, m)
	requireClass(t, getSeries(t, m, id), "", "")
}

func TestContentRatingPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	m := openUpgradeModule(t, path)
	a, b := addSeries(t, m, "Persist A"), addSeries(t, m, "Persist B")
	setRating(t, m, a, "TV-14", false)
	setRating(t, m, b, "", true)
	closeUpgradeModule(t, m)

	m = openUpgradeModule(t, path)
	defer closeUpgradeModule(t, m)
	requireClass(t, getSeries(t, m, a), "TV-14", "operator")
	requireClass(t, getSeries(t, m, b), "NR", "operator")
}

// noCeiling is the grid value for "empty max_rating" (no ceiling).
const noCeiling int32 = -1

// ceilingToken returns a max_rating token for ladder level 0..4, and "" for
// noCeiling.
func ceilingToken(level int32) string {
	if level == noCeiling {
		return ""
	}
	return []string{"G", "PG", "PG-13", "R", "NC-17"}[level]
}

type fixtureSeries struct {
	name   string
	rating string // "" = unavailable; "NR" = explicit unrated
	tags   []string
}

// filterFixture is a library covering every classification state.
var filterFixture = []fixtureSeries{
	{name: "A kids G", rating: "TV-Y"},
	{name: "B PG", rating: "TV-PG", tags: []string{"family"}},
	{name: "C teen", rating: "TV-14", tags: []string{"drama"}},
	{name: "D mature", rating: "TV-MA", tags: []string{"drama", "family"}},
	{name: "E adult", rating: "NC-17"},
	{name: "F unrated", rating: "NR", tags: []string{"family"}},
	{name: "G unavailable", rating: "", tags: []string{"family"}},
	{name: "H gore PG", rating: "PG", tags: []string{"Gore "}},
	{name: "I gorey G", rating: "G", tags: []string{"gorey"}},
	{name: "J accent", rating: "G", tags: []string{"Été"}},
	{name: "K bare", rating: "R"},
}

func seedFilterFixture(t *testing.T, m *Module) map[string]fixtureSeries {
	t.Helper()
	byID := map[string]fixtureSeries{}
	for _, f := range filterFixture {
		id := addSeries(t, m, f.name)
		switch f.rating {
		case "":
		case "NR":
			setRating(t, m, id, "", true)
		default:
			setRating(t, m, id, f.rating, false)
		}
		if len(f.tags) > 0 {
			tagSeries(t, m, id, f.tags...)
		}
		byID[id] = f
	}
	return byID
}

// oracleVisible is an independent restatement of the ADR-0031 Decision 2.6
// table, used to check the implementation over a grid of filters.
func oracleVisible(f fixtureSeries, level int32, allowUnrated bool, blocked, allowed []string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	switch f.rating {
	case "":
		return false
	case "NR":
		if !allowUnrated {
			return false
		}
	default:
		l, _ := ratingLevel(f.rating)
		if level != noCeiling && int32(l) > level {
			return false
		}
	}
	has := func(tag string) bool {
		for _, x := range f.tags {
			if norm(x) == norm(tag) {
				return true
			}
		}
		return false
	}
	for _, b := range blocked {
		if has(b) {
			return false
		}
	}
	if len(allowed) > 0 {
		ok := false
		for _, a := range allowed {
			ok = ok || has(a)
		}
		if !ok {
			return false
		}
	}
	return true
}

func listIDs(t *testing.T, m *Module, req *tvmgmtv1.ListTVShowsRequest) ([]string, int32) {
	t.Helper()
	r, err := m.ListTVShows(context.Background(), req)
	if err != nil {
		t.Fatalf("ListTVShows: %v", err)
	}
	var ids []string
	for _, s := range r.GetSeries() {
		ids = append(ids, s.GetId())
	}
	return ids, r.GetTotal()
}

func TestClassificationFilterNarrowsNeverWidens(t *testing.T) {
	m := newTestModule(t)
	byID := seedFilterFixture(t, m)

	all, allTotal := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100})
	if len(all) != len(filterFixture) || int(allTotal) != len(filterFixture) {
		t.Fatalf("unfiltered: %d series total %d", len(all), allTotal)
	}
	// Unset and disabled filters change nothing, whatever else they carry.
	for _, f := range []*tvmgmtv1.ClassificationFilter{
		nil, {}, {Enabled: false, MaxRating: "G", BlockedTags: []string{"family"}, AllowedTags: []string{"nothing"}},
	} {
		ids, total := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, ClassificationFilter: f})
		if !slices.Equal(ids, all) || total != allTotal {
			t.Errorf("filter %v changed the unfiltered result: %v total %d", f, ids, total)
		}
	}

	type tagSet = []string
	blockedOpts := []tagSet{nil, {"gore"}, {" GORE"}, {"family"}, {"família", "été"}, {"drama", "family"}}
	allowedOpts := []tagSet{nil, {"family"}, {"drama"}, {"ÉTÉ"}, {"nothing"}}
	cases := 0
	for level := noCeiling; level <= 4; level++ {
		for _, unrated := range []bool{false, true} {
			for _, blocked := range blockedOpts {
				for _, allowed := range allowedOpts {
					cases++
					req := &tvmgmtv1.ListTVShowsRequest{PageSize: 100, ClassificationFilter: &tvmgmtv1.ClassificationFilter{
						Enabled: true, MaxRating: ceilingToken(level), AllowUnrated: unrated, BlockedTags: blocked, AllowedTags: allowed,
					}}
					ids, total := listIDs(t, m, req)

					var want []string
					for _, id := range all {
						if oracleVisible(byID[id], level, unrated, blocked, allowed) {
							want = append(want, id)
						}
					}
					if !slices.Equal(ids, want) || int(total) != len(want) {
						t.Fatalf("level=%d unrated=%v blocked=%v allowed=%v: got %v total %d, want %v", level, unrated, blocked, allowed, ids, total, want)
					}
					for _, id := range ids {
						if !slices.Contains(all, id) {
							t.Fatalf("filter widened the result with %s", id)
						}
						if byID[id].rating == "" {
							t.Fatalf("unavailable series %q visible through an enabled filter", byID[id].name)
						}
					}
				}
			}
		}
	}
	t.Logf("%d filter combinations match the oracle", cases)
}

func TestClassificationFilterSpecificCases(t *testing.T) {
	m := newTestModule(t)
	byID := seedFilterFixture(t, m)
	names := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			out = append(out, strings.SplitN(byID[id].name, " ", 2)[0])
		}
		return out
	}
	run := func(f *tvmgmtv1.ClassificationFilter) []string {
		ids, _ := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, ClassificationFilter: f})
		return names(ids)
	}

	// An enabled filter with an empty ceiling has no rating limit, but still
	// hides unavailable (G) and unrated (F) series.
	if got, want := run(&tvmgmtv1.ClassificationFilter{Enabled: true}), []string{"A", "B", "C", "D", "E", "H", "I", "J", "K"}; !slices.Equal(got, want) {
		t.Errorf("enabled with no ceiling: %v, want %v", got, want)
	}
	// The ceiling token is case-insensitive and the lowest tier admits only it.
	if got, want := run(&tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: " tv-y7 "}), []string{"A", "I", "J"}; !slices.Equal(got, want) {
		t.Errorf("ceiling tv-y7: %v, want %v", got, want)
	}
	// Level 1 admits PG-tier, and "gore" blocks only the exact tag: "gorey" stays.
	if got, want := run(&tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG", BlockedTags: []string{"gore"}}), []string{"A", "B", "I", "J"}; !slices.Equal(got, want) {
		t.Errorf("level 1 blocked gore: %v, want %v", got, want)
	}
	// Non-ASCII blocked tag folds like Go, not like SQLite lower().
	if got := run(&tvmgmtv1.ClassificationFilter{Enabled: true, BlockedTags: []string{"ÉTÉ"}}); !slices.Equal(got, []string{"A", "B", "C", "D", "E", "H", "I", "K"}) {
		t.Errorf("unicode blocked tag: %v (J carries the tag \"Été\")", got)
	}
	// An allowed tag never unlocks a rating denial (D is TV-MA, ceiling PG-13).
	if got := run(&tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG-13", AllowedTags: []string{"family"}}); !slices.Equal(got, []string{"B"}) {
		t.Errorf("allowed family at PG-13: %v, want [B]", got)
	}
	// Blocked beats allowed.
	if got := run(&tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "R", AllowedTags: []string{"family"}, BlockedTags: []string{"drama"}}); !slices.Equal(got, []string{"B"}) {
		t.Errorf("blocked beats allowed: %v, want [B]", got)
	}
	// Explicit NR only with allow_unrated; unavailable (G) never.
	if got := run(&tvmgmtv1.ClassificationFilter{Enabled: true, AllowedTags: []string{"family"}}); !slices.Equal(got, []string{"B", "D"}) {
		t.Errorf("family without allow_unrated: %v, want [B D]", got)
	}
	if got := run(&tvmgmtv1.ClassificationFilter{Enabled: true, AllowedTags: []string{"family"}, AllowUnrated: true}); !slices.Equal(got, []string{"B", "D", "F"}) {
		t.Errorf("family with allow_unrated: %v, want [B D F]", got)
	}
}

func TestClassificationFilterRejectsInvalid(t *testing.T) {
	m := newTestModule(t)
	seedFilterFixture(t, m)
	for name, f := range map[string]*tvmgmtv1.ClassificationFilter{
		"unknown token":     {Enabled: true, MaxRating: "15"},
		"12A":               {Enabled: true, MaxRating: "12A"},
		"NR is not a level": {Enabled: true, MaxRating: "NR"},
		"UR is not a level": {Enabled: true, MaxRating: "UR"},
		"typo":              {Enabled: true, MaxRating: "PG13"},
		"empty blocked tag": {Enabled: true, MaxRating: "R", BlockedTags: []string{"ok", "  "}},
		"empty allowed tag": {Enabled: true, MaxRating: "R", AllowedTags: []string{""}},
	} {
		_, err := m.ListTVShows(context.Background(), &tvmgmtv1.ListTVShowsRequest{ClassificationFilter: f})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	// Invalid values on a disabled filter are not evaluated.
	if _, err := m.ListTVShows(context.Background(), &tvmgmtv1.ListTVShowsRequest{ClassificationFilter: &tvmgmtv1.ClassificationFilter{MaxRating: "bogus", BlockedTags: []string{""}}}); err != nil {
		t.Errorf("disabled filter rejected: %v", err)
	}
}

func TestClassificationFilterTotalAndPagination(t *testing.T) {
	m := newTestModule(t)
	byID := seedFilterFixture(t, m)
	f := &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "R", AllowUnrated: true}

	var want []string
	all, _ := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100})
	for _, id := range all {
		if oracleVisible(byID[id], 3, true, nil, nil) {
			want = append(want, id)
		}
	}
	if len(want) < 6 {
		t.Fatalf("fixture too small: %d visible", len(want))
	}

	// Page through two at a time: total stays the visible count on every page,
	// pages concatenate to exactly the visible set, in order, with no gaps.
	var got []string
	pages := 0
	for p := int32(1); p <= 10; p++ {
		ids, total := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{Page: p, PageSize: 2, ClassificationFilter: f})
		if int(total) != len(want) {
			t.Fatalf("page %d total = %d, want %d (visible only)", p, total, len(want))
		}
		if len(ids) > 2 {
			t.Fatalf("page %d has %d items", p, len(ids))
		}
		got = append(got, ids...)
		if len(ids) > 0 {
			pages++
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
	if pages != (len(want)+1)/2 {
		t.Errorf("non-empty pages = %d, want %d", pages, (len(want)+1)/2)
	}

	// Descending sort and search still compose with the filter.
	desc, _ := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, SortOrder: "desc", ClassificationFilter: f})
	slices.Reverse(want)
	if !slices.Equal(desc, want) {
		t.Errorf("desc = %v, want %v", desc, want)
	}
	ids, total := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, Search: "unavailable", ClassificationFilter: f})
	if len(ids) != 0 || total != 0 {
		t.Errorf("search for the unavailable series through the filter: %v total %d", ids, total)
	}
	ids, total = listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, Search: "mature", ClassificationFilter: f})
	if len(ids) != 1 || total != 1 {
		t.Errorf("search mature: %v total %d", ids, total)
	}
	// Filter and tag_id both narrow.
	tags, _ := m.ListTags(context.Background(), &tvmgmtv1.ListTagsRequest{})
	for _, tg := range tags.GetTags() {
		if tg.GetLabel() != "drama" {
			continue
		}
		ids, total := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, TagId: tg.GetId(), ClassificationFilter: &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG-13"}})
		if len(ids) != 1 || total != 1 || byID[ids[0]].rating != "TV-14" {
			t.Errorf("drama tag at level 2: %v total %d", ids, total)
		}
	}
}

// TestClassificationFilterRaceClean runs rating and tag writers against
// filtered and unfiltered readers; run under -race. Every series a filtered
// read returns must pass the filter as it is reported.
func TestClassificationFilterRaceClean(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	var ids []string
	for i := range 8 {
		ids = append(ids, addSeries(t, m, fmt.Sprintf("Race %d", i)))
	}
	tagA, err := m.CreateTag(ctx, &tvmgmtv1.CreateTagRequest{Label: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	cf := &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG", AllowUnrated: true, BlockedTags: []string{"blocked"}}
	compiled, err := compileClassificationFilter(cf)
	if err != nil {
		t.Fatal(err)
	}

	ratings := []string{"G", "TV-PG", "TV-14", "TV-MA", "", "NR"}
	const iters = 60
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}

	for w := range 2 {
		wg.Go(func() {
			for i := range iters {
				id := ids[(i+w)%len(ids)]
				r := ratings[(i*3+w)%len(ratings)]
				_, err := m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: id, ContentRating: strings.TrimPrefix(r, "NR"), ExplicitUnrated: r == "NR"})
				if err != nil {
					report(fmt.Errorf("SetContentRating: %w", err))
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := range iters {
			var tids []string
			if i%2 == 0 {
				tids = []string{tagA.GetTagId()}
			}
			if _, err := m.SetItemTags(ctx, &tvmgmtv1.SetItemTagsRequest{ItemId: ids[i%len(ids)], TagIds: tids}); err != nil {
				report(fmt.Errorf("SetItemTags: %w", err))
				return
			}
		}
	})
	for range 3 {
		wg.Go(func() {
			for range iters {
				r, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, ClassificationFilter: cf})
				if err != nil {
					report(fmt.Errorf("filtered list: %w", err))
					return
				}
				for _, s := range r.GetSeries() {
					if !compiled.visible(s.GetContentRating(), s.GetTagLabels()) {
						report(fmt.Errorf("returned %s with rating %q tags %v, which the filter hides", s.GetName(), s.GetContentRating(), s.GetTagLabels()))
						return
					}
				}
				if _, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{PageSize: 100}); err != nil {
					report(fmt.Errorf("list: %w", err))
					return
				}
				if _, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: ids[0]}); err != nil {
					report(fmt.Errorf("get: %w", err))
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
