package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
	"github.com/Muxcore-Media/core/sdk/go/module/moduletest"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ADR-0031 section 2.2: the lower-precedence "tmdb" classification source.

// fakeMetadata is a metadata client double. Only GetTVDetails and
// GetSeasonDetails are implemented; any other call panics on the nil embedded
// interface, which would flag an unexpected dependency.
type fakeMetadata struct {
	metadatav1.MetadataServiceClient

	mu sync.Mutex
	// cert is the certification returned for each tmdb id; ids not present
	// answer NotFound.
	cert map[int32]string
	// fail, when set, makes every GetTVDetails return that error.
	fail error
	// gate, when set, is received from before GetTVDetails answers, so a test
	// can hold a refresh in flight.
	gate  chan struct{}
	calls atomic.Int32
}

func (f *fakeMetadata) GetTVDetails(ctx context.Context, req *metadatav1.GetTVDetailsRequest, _ ...grpc.CallOption) (*metadatav1.GetTVDetailsResponse, error) {
	f.calls.Add(1)
	f.mu.Lock()
	gate, fail := f.gate, f.fail
	cert, ok := f.cert[req.GetId()]
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail != nil {
		return nil, fail
	}
	if !ok {
		return nil, status.Error(codes.NotFound, "no such show")
	}
	return &metadatav1.GetTVDetailsResponse{
		Id:            req.GetId(),
		Name:          fmt.Sprintf("Show %d", req.GetId()),
		Certification: cert,
		// Country is informational on the wire; the module must not depend on it.
		CertificationCountry: "US",
	}, nil
}

func (f *fakeMetadata) GetSeasonDetails(context.Context, *metadatav1.GetSeasonDetailsRequest, ...grpc.CallOption) (*metadatav1.GetSeasonDetailsResponse, error) {
	return &metadatav1.GetSeasonDetailsResponse{}, nil
}

func (f *fakeMetadata) set(tmdbID int32, cert string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cert == nil {
		f.cert = map[int32]string{}
	}
	f.cert[tmdbID] = cert
}

func (f *fakeMetadata) setFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

func installFakeMetadata(m *Module, f *fakeMetadata) {
	var factory metadataClientFactory = func(context.Context) (metadatav1.MetadataServiceClient, func(), error) {
		return f, func() {}, nil
	}
	m.metadataClientOverride.Store(&factory)
}

// insertTMDBSeries inserts a series straight into the table. It deliberately
// avoids AddTVShow, which starts background metadata work that would race with
// the fake.
func insertTMDBSeries(t *testing.T, m *Module, id string, tmdbID int32) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := m.dbConn().ExecContext(context.Background(),
		`INSERT INTO series (id, tmdb_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, tmdbID, "Show "+id, now, now); err != nil {
		t.Fatalf("insert series %s: %v", id, err)
	}
}

func refresh(t *testing.T, m *Module, id string) {
	t.Helper()
	if _, err := m.RefreshMetadata(context.Background(), &tvmgmtv1.RefreshMetadataRequest{SeriesId: id}); err != nil {
		t.Fatalf("RefreshMetadata(%s): %v", id, err)
	}
}

func storedTMDB(t *testing.T, m *Module, id string) string {
	t.Helper()
	var v string
	if err := m.dbConn().QueryRowContext(context.Background(), `SELECT parental_rating_tmdb FROM series WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNormalizeTMDBCertification(t *testing.T) {
	for in, want := range map[string]string{
		// every ladder token, in its canonical spelling
		"G": "G", "TV-Y": "TV-Y", "TV-Y7": "TV-Y7", "TV-Y7-FV": "TV-Y7-FV", "ALL": "ALL", "E": "E",
		"PG": "PG", "TV-G": "TV-G", "TV-PG": "TV-PG", "E10+": "E10+",
		"PG-13": "PG-13", "TV-14": "TV-14", "T": "T",
		"R": "R", "TV-MA": "TV-MA", "M": "M", "MA": "MA",
		"NC-17": "NC-17", "AO": "AO", "X": "X",
		// trimmed and upper-cased
		"  tv-ma ": "TV-MA", "pg-13": "PG-13", "\ttv-y7-fv\n": "TV-Y7-FV", "e10+": "E10+",
		// unrated markers become the canonical NR
		"NR": "NR", "nr": "NR", "UR": "NR", "ur": "NR", "Not Rated": "NR", " NOT RATED ": "NR", "unrated": "NR", "UNRATED": "NR",
		// anything else is no value: country tokens, free text, empty, near misses
		"": "", "   ": "", "15": "", "12A": "", "18": "", "16": "", "U": "", "FSK 16": "", "ab 12": "",
		"TV-MA (US)": "", "PG13": "", "NC17": "", "TV-13": "", "NOT  RATED": "", "NOTRATED": "",
		"Rated R": "", "N/A": "", "0": "", "TV-": "", "-": "",
	} {
		if got := normalizeTMDBCertification(in); got != want {
			t.Errorf("normalizeTMDBCertification(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEffectiveRatingPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		opRating, opSource, tmd string
		wantRating, wantSource  string
	}{
		{"none", "", "", "", "", ""},
		{"tmdb only", "", "", "TV-MA", "TV-MA", "tmdb"},
		{"tmdb NR only", "", "", "NR", "NR", "tmdb"},
		{"operator only", "TV-14", "operator", "", "TV-14", "operator"},
		{"operator beats different tmdb", "TV-14", "operator", "TV-MA", "TV-14", "operator"},
		{"operator lower than tmdb still wins", "TV-Y", "operator", "TV-MA", "TV-Y", "operator"},
		{"operator NR beats tmdb rating", "NR", "operator", "TV-MA", "NR", "operator"},
		{"operator rating beats tmdb NR", "PG", "operator", "NR", "PG", "operator"},
		{"tampered tmdb is unavailable", "", "", "15", "", ""},
		{"tampered tmdb garbage", "", "", "drop table", "", ""},
		{"tmdb stored lower-case still reads", "", "", " tv-14 ", "TV-14", "tmdb"},
		// A bad operator record must not fall through to a weaker source.
		{"tampered operator rating fails closed", "15", "operator", "TV-G", "", ""},
		{"wrong operator source fails closed", "TV-MA", "tmdb", "TV-G", "", ""},
		{"operator source without rating fails closed", "", "operator", "TV-G", "", ""},
		{"operator rating without source fails closed", "TV-MA", "", "TV-G", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := effectiveRating(tc.opRating, tc.opSource, tc.tmd)
			if r != tc.wantRating || s != tc.wantSource {
				t.Errorf("effectiveRating(%q,%q,%q) = %q/%q, want %q/%q", tc.opRating, tc.opSource, tc.tmd, r, s, tc.wantRating, tc.wantSource)
			}
		})
	}
}

// TestPrecedenceMatrixBothOrders runs the operator-versus-tmdb matrix through the
// real write paths (SetContentRating and RefreshMetadata) in both orders, so the
// result cannot depend on timing.
func TestPrecedenceMatrixBothOrders(t *testing.T) {
	type op struct {
		rating  string
		unrated bool
		set     bool
	}
	cases := []struct {
		name       string
		op         op
		cert       string
		wantRating string
		wantSource string
	}{
		{"operator rated, tmdb different", op{rating: "TV-14", set: true}, "TV-MA", "TV-14", "operator"},
		{"operator rated lower, tmdb higher", op{rating: "G", set: true}, "NC-17", "G", "operator"},
		{"operator rated higher, tmdb lower", op{rating: "TV-MA", set: true}, "TV-Y", "TV-MA", "operator"},
		{"operator NR, tmdb TV-MA", op{unrated: true, set: true}, "TV-MA", "NR", "operator"},
		{"operator rated, tmdb unmappable", op{rating: "PG", set: true}, "15", "PG", "operator"},
		{"operator rated, tmdb empty", op{rating: "PG", set: true}, "", "PG", "operator"},
		{"no operator, tmdb TV-MA", op{}, "TV-MA", "TV-MA", "tmdb"},
		{"no operator, tmdb NR", op{}, "Not Rated", "NR", "tmdb"},
		{"no operator, tmdb unmappable", op{}, "12A", "", ""},
		{"no operator, tmdb empty", op{}, "", "", ""},
	}
	for _, order := range []string{"operator-then-refresh", "refresh-then-operator"} {
		for i, tc := range cases {
			t.Run(order+"/"+tc.name, func(t *testing.T) {
				m := newTestModule(t)
				f := &fakeMetadata{}
				installFakeMetadata(m, f)
				id, tmdbID := fmt.Sprintf("s%d", i), int32(100+i)
				insertTMDBSeries(t, m, id, tmdbID)
				f.set(tmdbID, tc.cert)

				apply := func() {
					if tc.op.set {
						setRating(t, m, id, tc.op.rating, tc.op.unrated)
					}
				}
				if order == "operator-then-refresh" {
					apply()
					refresh(t, m, id)
				} else {
					refresh(t, m, id)
					apply()
				}
				requireClass(t, getSeries(t, m, id), tc.wantRating, tc.wantSource)

				// The list path agrees with the detail path.
				list, err := m.ListTVShows(context.Background(), &tvmgmtv1.ListTVShowsRequest{})
				if err != nil || len(list.GetSeries()) != 1 {
					t.Fatalf("list = %v, %v", list, err)
				}
				requireClass(t, list.GetSeries()[0], tc.wantRating, tc.wantSource)
			})
		}
	}
}

func TestOperatorClearFallsBackToTMDB(t *testing.T) {
	m := newTestModule(t)
	f := &fakeMetadata{}
	installFakeMetadata(m, f)
	insertTMDBSeries(t, m, "s1", 7)
	f.set(7, "TV-PG")

	refresh(t, m, "s1")
	requireClass(t, getSeries(t, m, "s1"), "TV-PG", "tmdb")

	setRating(t, m, "s1", "R", false)
	requireClass(t, getSeries(t, m, "s1"), "R", "operator")

	setRating(t, m, "s1", "", false) // clear
	requireClass(t, getSeries(t, m, "s1"), "TV-PG", "tmdb")

	setRating(t, m, "s1", "", true) // explicit NR hides the tmdb rating
	requireClass(t, getSeries(t, m, "s1"), "NR", "operator")

	setRating(t, m, "s1", "", false)
	requireClass(t, getSeries(t, m, "s1"), "TV-PG", "tmdb")

	// Clearing never touches the stored tmdb value.
	if got := storedTMDB(t, m, "s1"); got != "TV-PG" {
		t.Errorf("stored tmdb = %q after operator clear", got)
	}

	// No tmdb value: clearing returns to unavailable.
	insertTMDBSeries(t, m, "s2", 8)
	setRating(t, m, "s2", "G", false)
	setRating(t, m, "s2", "", false)
	requireClass(t, getSeries(t, m, "s2"), "", "")
}

func TestRefreshSemantics(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	f := &fakeMetadata{}
	installFakeMetadata(m, f)
	insertTMDBSeries(t, m, "s1", 7)

	f.set(7, "TV-14")
	refresh(t, m, "s1")
	requireClass(t, getSeries(t, m, "s1"), "TV-14", "tmdb")

	// An errored metadata call keeps the previously stored valid value.
	f.setFail(status.Error(codes.Unavailable, "metadata down"))
	if _, err := m.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: "s1"}); err == nil {
		t.Fatal("RefreshMetadata succeeded against a failing metadata service")
	}
	requireClass(t, getSeries(t, m, "s1"), "TV-14", "tmdb")

	// So does a metadata service that cannot be reached at all.
	var unreachable metadataClientFactory = func(context.Context) (metadatav1.MetadataServiceClient, func(), error) {
		return nil, nil, errors.New("no metadata module found")
	}
	m.metadataClientOverride.Store(&unreachable)
	if _, err := m.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: "s1"}); err == nil {
		t.Fatal("RefreshMetadata succeeded with no metadata module")
	}
	requireClass(t, getSeries(t, m, "s1"), "TV-14", "tmdb")

	// The same through the import-driven season population path.
	installFakeMetadata(m, f)
	f.setFail(errors.New("boom"))
	m.populateSeasonsFromMetadata(ctx, "s1", 7)
	requireClass(t, getSeries(t, m, "s1"), "TV-14", "tmdb")
	f.setFail(nil)

	// A successful call replaces the value ...
	f.set(7, "tv-ma")
	refresh(t, m, "s1")
	requireClass(t, getSeries(t, m, "s1"), "TV-MA", "tmdb")

	// ... a successful call with an unmappable certification clears it ...
	f.set(7, "15")
	refresh(t, m, "s1")
	requireClass(t, getSeries(t, m, "s1"), "", "")
	if got := storedTMDB(t, m, "s1"); got != "" {
		t.Errorf("stored tmdb = %q after unmappable certification, want empty", got)
	}

	// ... and so does a successful call with no certification at all.
	f.set(7, "PG")
	refresh(t, m, "s1")
	requireClass(t, getSeries(t, m, "s1"), "PG", "tmdb")
	f.set(7, "")
	refresh(t, m, "s1")
	requireClass(t, getSeries(t, m, "s1"), "", "")

	// populateSeasonsFromMetadata applies the same success semantics.
	f.set(7, "TV-G")
	m.populateSeasonsFromMetadata(ctx, "s1", 7)
	requireClass(t, getSeries(t, m, "s1"), "TV-G", "tmdb")
	f.set(7, "")
	m.populateSeasonsFromMetadata(ctx, "s1", 7)
	requireClass(t, getSeries(t, m, "s1"), "", "")

	// An unknown show (NotFound) is an error, not "no certification".
	f.set(7, "R")
	refresh(t, m, "s1")
	f.mu.Lock()
	delete(f.cert, 7)
	f.mu.Unlock()
	if _, err := m.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: "s1"}); err == nil {
		t.Fatal("RefreshMetadata succeeded for a show metadata does not know")
	}
	requireClass(t, getSeries(t, m, "s1"), "R", "tmdb")
}

// TestTMDBNotWrittenByOtherPaths: only a successful GetTVDetails writes the tmdb
// value. AddTVShow, UpdateMetadata and a certification-looking metadata map do
// not.
func TestTMDBNotWrittenByOtherPaths(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	id := addSeries(t, m, "Other Paths")
	if _, err := m.dbConn().ExecContext(ctx, `UPDATE series SET vote_average = 9.9, genres = '["Kids"]' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	tagSeries(t, m, id, "tv-y", "G")
	requireClass(t, getSeries(t, m, id), "", "")
	if got := storedTMDB(t, m, id); got != "" {
		t.Errorf("stored tmdb = %q", got)
	}
}

func TestTamperedTMDBValueReadsUnavailable(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	insertTMDBSeries(t, m, "s1", 1)
	for _, bad := range []string{"15", "12A", "UR", "garbage", "TV-MA\x00", "TVMA", "tmdb", "  "} {
		if _, err := m.dbConn().ExecContext(ctx, `UPDATE series SET parental_rating_tmdb = ? WHERE id = 's1'`, bad); err != nil {
			t.Fatal(err)
		}
		requireClass(t, getSeries(t, m, "s1"), "", "")
		l, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{
			ClassificationFilter: &tvmgmtv1.ClassificationFilter{Enabled: true, AllowUnrated: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if l.GetTotal() != 0 || len(l.GetSeries()) != 0 {
			t.Errorf("tampered tmdb value %q visible through the filter", bad)
		}
	}

	// A tampered operator row fails closed even when a valid tmdb value exists.
	if _, err := m.dbConn().ExecContext(ctx,
		`UPDATE series SET parental_rating_tmdb = 'G', parental_rating = '15', parental_rating_source = 'operator' WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	requireClass(t, getSeries(t, m, "s1"), "", "")
}

// TestFilterOnTMDBSourcedSeries: the classification filter treats a tmdb rating
// exactly like an operator one, and unavailable stays hidden.
func TestFilterOnTMDBSourcedSeries(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	f := &fakeMetadata{}
	installFakeMetadata(m, f)
	certs := map[string]string{
		"kids":   "TV-Y",
		"teen":   "TV-14",
		"adult":  "TV-MA",
		"nr":     "NR",
		"none":   "",
		"abroad": "15",
	}
	tmdbOf := map[string]int32{}
	n := int32(500)
	for id, cert := range certs {
		n++
		tmdbOf[id] = n
		insertTMDBSeries(t, m, id, n)
		f.set(n, cert)
		refresh(t, m, id)
	}
	// operator overrides: a tmdb-adult show the operator rates G, and a tmdb-kids
	// show the operator rates R.
	insertTMDBSeries(t, m, "op-low", 900)
	f.set(900, "TV-MA")
	refresh(t, m, "op-low")
	setRating(t, m, "op-low", "G", false)
	insertTMDBSeries(t, m, "op-high", 901)
	f.set(901, "TV-Y")
	refresh(t, m, "op-high")
	setRating(t, m, "op-high", "R", false)

	visible := func(cf *tvmgmtv1.ClassificationFilter) []string {
		ids, total := listIDs(t, m, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, ClassificationFilter: cf})
		if int(total) != len(ids) {
			t.Errorf("total %d != %d returned for %v", total, len(ids), cf)
		}
		slices.Sort(ids)
		return ids
	}
	for _, tc := range []struct {
		name string
		cf   *tvmgmtv1.ClassificationFilter
		want []string
	}{
		{"disabled lists everything", &tvmgmtv1.ClassificationFilter{MaxRating: "G"}, []string{"abroad", "adult", "kids", "none", "nr", "op-high", "op-low", "teen"}},
		{"ceiling G", &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "G"}, []string{"kids", "op-low"}},
		{"ceiling G allow unrated", &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "G", AllowUnrated: true}, []string{"kids", "nr", "op-low"}},
		{"ceiling PG-13", &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG-13"}, []string{"kids", "op-low", "teen"}},
		{"ceiling TV-MA", &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "TV-MA"}, []string{"adult", "kids", "op-high", "op-low", "teen"}},
		{"no ceiling allow unrated", &tvmgmtv1.ClassificationFilter{Enabled: true, AllowUnrated: true}, []string{"adult", "kids", "nr", "op-high", "op-low", "teen"}},
		{"no ceiling disallow unrated", &tvmgmtv1.ClassificationFilter{Enabled: true}, []string{"adult", "kids", "op-high", "op-low", "teen"}},
	} {
		if got := visible(tc.cf); !slices.Equal(got, tc.want) {
			t.Errorf("%s: visible = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Tags still apply on top of a tmdb rating, and never override a denial.
	tagSeries(t, m, "kids", "gore")
	tagSeries(t, m, "adult", "ok")
	got := visible(&tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG-13", BlockedTags: []string{"gore"}})
	if want := []string{"op-low", "teen"}; !slices.Equal(got, want) {
		t.Errorf("blocked tag: visible = %v, want %v", got, want)
	}
	got = visible(&tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "PG-13", AllowedTags: []string{"ok"}})
	if len(got) != 0 {
		t.Errorf("allowed tag must not unlock a rating denial: visible = %v", got)
	}

	// A series whose tmdb value is later cleared by a refresh drops out.
	f.set(tmdbOf["kids"], "")
	refresh(t, m, "kids")
	got = visible(&tvmgmtv1.ClassificationFilter{Enabled: true, AllowUnrated: true})
	if slices.Contains(got, "kids") {
		t.Errorf("kids still visible after its tmdb value was cleared: %v", got)
	}
	_ = ctx
}

// TestEpisodesInheritEffectiveSeriesRating: an episode id resolves (GetEpisode)
// to its series, whose effective classification is what applies, whichever
// source supplied it.
func TestEpisodesInheritEffectiveSeriesRating(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	f := &fakeMetadata{}
	installFakeMetadata(m, f)
	insertTMDBSeries(t, m, "s1", 7)
	insertEpisode(t, m, "s1", "sea1", "ep1", 1)
	f.set(7, "TV-14")
	refresh(t, m, "s1")

	classOfEpisode := func() (string, string) {
		t.Helper()
		e, err := m.GetEpisode(ctx, &tvmgmtv1.GetEpisodeRequest{EpisodeId: "ep1"})
		if err != nil {
			t.Fatal(err)
		}
		s := getSeries(t, m, e.GetEpisode().GetSeriesId())
		return s.GetContentRating(), s.GetContentRatingSource()
	}
	if r, s := classOfEpisode(); r != "TV-14" || s != "tmdb" {
		t.Errorf("episode inherits %q/%q, want TV-14/tmdb", r, s)
	}
	setRating(t, m, "s1", "PG", false)
	if r, s := classOfEpisode(); r != "PG" || s != "operator" {
		t.Errorf("episode inherits %q/%q, want PG/operator", r, s)
	}
	setRating(t, m, "s1", "", false)
	if r, s := classOfEpisode(); r != "TV-14" || s != "tmdb" {
		t.Errorf("episode inherits %q/%q after operator clear, want TV-14/tmdb", r, s)
	}
	f.set(7, "")
	refresh(t, m, "s1")
	if r, s := classOfEpisode(); r != "" || s != "" {
		t.Errorf("episode inherits %q/%q, want unavailable", r, s)
	}
	// Seasons and episodes carry no classification of their own.
	full := getSeries(t, m, "s1")
	for _, sea := range full.GetSeasons() {
		if sea.GetId() == "" {
			t.Error("season without id")
		}
	}
}

// TestRefreshInFlightCannotHideOperator holds a refresh in flight, records an
// operator rating, then lets the refresh finish with a different certification:
// the operator value must still win.
func TestRefreshInFlightCannotHideOperator(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	f := &fakeMetadata{gate: make(chan struct{})}
	installFakeMetadata(m, f)
	insertTMDBSeries(t, m, "s1", 7)
	f.set(7, "TV-MA")

	done := make(chan error, 1)
	go func() {
		_, err := m.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: "s1"})
		done <- err
	}()
	for f.calls.Load() == 0 { // refresh is now blocked in GetTVDetails
		time.Sleep(time.Millisecond)
	}
	setRating(t, m, "s1", "TV-Y", false)
	close(f.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	requireClass(t, getSeries(t, m, "s1"), "TV-Y", "operator")
	if got := storedTMDB(t, m, "s1"); got != "TV-MA" {
		t.Errorf("tmdb value = %q, want the refreshed TV-MA stored beneath the operator value", got)
	}
}

// TestRefreshRacesSetContentRating runs refreshes (with changing, empty,
// unmappable and failing answers) against operator writes and reads, under
// -race. Series A is operator-rated before the storm: every read must show that
// operator value throughout. Series B has its operator value toggled: every read
// must be a coherent state, and once the writers stop the operator value wins.
func TestRefreshRacesSetContentRating(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	f := &fakeMetadata{}
	installFakeMetadata(m, f)
	insertTMDBSeries(t, m, "A", 1)
	insertTMDBSeries(t, m, "B", 2)
	f.set(1, "TV-MA")
	f.set(2, "TV-14")
	setRating(t, m, "A", "TV-Y", false)

	certs := []string{"TV-MA", "", "15", "NR", "G", "R"}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	const iters = 60

	for w := range 3 {
		wg.Go(func() {
			for i := range iters {
				id, tid := "A", int32(1)
				if (i+w)%2 == 1 {
					id, tid = "B", 2
				}
				f.set(tid, certs[(i+w)%len(certs)])
				f.setFail(nil)
				if i%7 == 0 {
					f.setFail(errors.New("flap"))
				}
				// Errors are expected while the fake flaps; they must not clear anything.
				_, _ = m.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: id})
				m.populateSeasonsFromMetadata(ctx, id, tid)
			}
		})
	}
	wg.Go(func() {
		for i := range iters {
			var err error
			switch i % 3 {
			case 0:
				_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: "B", ContentRating: "PG-13"})
			case 1:
				_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: "B"})
			default:
				_, err = m.SetContentRating(ctx, &tvmgmtv1.SetContentRatingRequest{SeriesId: "B", ExplicitUnrated: true})
			}
			if err != nil {
				report(fmt.Errorf("SetContentRating: %w", err))
				return
			}
		}
	})
	cf := &tvmgmtv1.ClassificationFilter{Enabled: true, MaxRating: "G"}
	for range 2 {
		wg.Go(func() {
			for range iters {
				a, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: "A"})
				if err != nil {
					report(err)
					return
				}
				if s := a.GetSeries(); s.GetContentRating() != "TV-Y" || s.GetContentRatingSource() != "operator" {
					report(fmt.Errorf("operator-rated series A read as %q/%q", s.GetContentRating(), s.GetContentRatingSource()))
					return
				}
				b, err := m.GetTVShow(ctx, &tvmgmtv1.GetTVShowRequest{SeriesId: "B"})
				if err != nil {
					report(err)
					return
				}
				switch s := b.GetSeries(); {
				case s.GetContentRatingSource() == "operator" && (s.GetContentRating() == "PG-13" || s.GetContentRating() == "NR"):
				case s.GetContentRatingSource() == "tmdb" && trustedTMDBRating(s.GetContentRating()) != "":
				case s.GetContentRatingSource() == "" && s.GetContentRating() == "":
				default:
					report(fmt.Errorf("series B incoherent: %q/%q", s.GetContentRating(), s.GetContentRatingSource()))
					return
				}
				// A is operator TV-Y (level 0): always visible under a G ceiling,
				// whatever the refreshes do to its tmdb value.
				l, err := m.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{PageSize: 100, ClassificationFilter: cf})
				if err != nil {
					report(err)
					return
				}
				if !slices.ContainsFunc(l.GetSeries(), func(s *tvmgmtv1.TVSeries) bool { return s.GetId() == "A" }) {
					report(errors.New("operator-rated series A hidden from the filtered list"))
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

	// Writers stopped: record an operator value last, then refresh again; it wins.
	f.setFail(nil)
	setRating(t, m, "B", "PG", false)
	f.set(2, "TV-MA")
	refresh(t, m, "B")
	requireClass(t, getSeries(t, m, "B"), "PG", "operator")
	requireClass(t, getSeries(t, m, "A"), "TV-Y", "operator")
}

func TestUpgradeThenTMDBRating(t *testing.T) {
	for _, tag := range upgradeSnapshots {
		t.Run(tag, func(t *testing.T) {
			path := moduletest.CopyFixture(t, filepath.Join("testdata", "upgrade", tag+".db"))
			m := openUpgradeModule(t, path)
			defer closeUpgradeModule(t, m)
			f := &fakeMetadata{}
			installFakeMetadata(m, f)

			// Upgraded rows carry no tmdb value.
			for _, id := range []string{"ser_1", "ser_2"} {
				if got := storedTMDB(t, m, id); got != "" {
					t.Errorf("%s: parental_rating_tmdb = %q after upgrade, want empty", id, got)
				}
			}
			before1 := getSeries(t, m, "ser_1")
			wantOp := upgradeOperatorRatings[tag]["ser_1"]

			// ser_2 (never operator-classified) gains a tmdb rating on refresh;
			// ser_1 keeps whatever the operator recorded, and gains one if it had none.
			f.set(37854, "TV-14")
			f.set(1668, "TV-MA")
			refresh(t, m, "ser_2")
			refresh(t, m, "ser_1")
			requireClass(t, getSeries(t, m, "ser_2"), "TV-14", "tmdb")
			if wantOp != "" {
				requireClass(t, getSeries(t, m, "ser_1"), wantOp, "operator")
			} else {
				requireClass(t, getSeries(t, m, "ser_1"), "TV-MA", "tmdb")
			}
			if after := getSeries(t, m, "ser_1"); after.GetTmdbId() != before1.GetTmdbId() {
				t.Errorf("refresh changed tmdb_id")
			}
		})
	}
}

// TestBusyTimeoutOnEveryPooledConnection: busy_timeout is per connection, so it
// must come from the DSN, not only from the PRAGMA run once in Init.
func TestBusyTimeoutOnEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	m := newTestModule(t)
	var conns []*sql.Conn
	for range sqliteMaxOpenConns {
		c, err := m.dbConn().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var ms int
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&ms); err != nil || ms != 5000 {
			t.Errorf("connection %d busy_timeout = %d, %v; want 5000", len(conns), ms, err)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}
