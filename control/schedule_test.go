package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kr/pretty"
	"golang.org/x/exp/slices"
	"golang.org/x/sync/errgroup"
	"kr.dev/diff"
	"kr.dev/errorfmt"
	"tier.run/refs"
)

var (
	mpf = refs.MustParseFeaturePlan
	mpp = refs.MustParsePlan
	mpn = refs.MustParseName
)

// interesting times to be in
var (
	t0 = time.Date(2020, 1, 0, 0, 0, 0, 0, time.UTC)
	t1 = time.Date(2020, 2, 0, 0, 0, 0, 0, time.UTC)
	t2 = time.Date(2020, 3, 0, 0, 0, 0, 0, time.UTC)
)

var ignoreProviderIDs = diff.OptionList(
	diff.ZeroFields[Feature]("ProviderID"),
	diff.ZeroFields[Org]("ProviderID"),
)

func TestSchedule(t *testing.T) {
	ciOnly(t)

	c := newTestClient(t)
	ctx := context.Background()

	var model []Feature
	plan := func(ff []Feature) []refs.FeaturePlan {
		model = append(model, ff...)
		var fps []refs.FeaturePlan
		for _, f := range ff {
			fps = append(fps, f.FeaturePlan)
		}
		return fps
	}

	planFree := plan([]Feature{{
		FeaturePlan: mpf("feature:x@plan:free@0"),
		Interval:    "@monthly",
		Currency:    "usd",
	}})

	planPro := plan([]Feature{{
		FeaturePlan: mpf("feature:x@plan:pro@0"),
		Interval:    "@monthly",
		Base:        100,
		Currency:    "usd",
	}})

	c.Push(ctx, model, pushLogger(t))

	sub := func(org string, fs []refs.FeaturePlan) {
		t.Helper()
		t.Logf("subscribing %s to %# v", org, pretty.Formatter(fs))

		if err := c.SubscribeTo(ctx, org, fs); err != nil {
			t.Fatalf("%# v", pretty.Formatter(err))
		}
	}

	check := func(org string, want []Phase) {
		t.Helper()
		got, err := c.LookupPhases(ctx, org)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("got phases %# v", pretty.Formatter(got))
		diff.Test(t, t.Errorf, got, want, ignoreProviderIDs)
	}

	clock := c.setClock(t, t0)
	sub("org:example", planFree)
	check("org:example", []Phase{{
		Org:       "org:example",
		Current:   true,
		Effective: t0,
		Features:  planFree,
		Plans:     plans("plan:free@0"),
	}})

	clock.Advance(t1)
	sub("org:example", planPro)
	check("org:example", []Phase{
		{
			Org:       "org:example",
			Current:   false,
			Effective: t0, // unchanged by advanced clock
			Features:  planFree,
			Plans:     plans("plan:free@0"),
		},
		{
			Org:       "org:example",
			Current:   true,
			Effective: t1, // unchanged by advanced clock
			Features:  planPro,
			Plans:     plans("plan:pro@0"),
		},
	})

	// downgrade and check no new phases
	sub("org:example", planFree)
	check("org:example", []Phase{
		{
			Org:       "org:example",
			Current:   false,
			Effective: t0, // unchanged by advanced clock
			Features:  planFree,
			Plans:     plans("plan:free@0"),
		},
		{
			Org:       "org:example",
			Current:   true,
			Effective: t1, // unchanged by advanced clock
			Features:  planFree,
			Plans:     plans("plan:free@0"),
		},
	})
}

func TestScheduleUpdateOrgOnSchedule(t *testing.T) {
	info := &OrgInfo{Email: "test@foo.com"}
	c := newTestClient(t)
	ctx := context.Background()
	c.Push(ctx, []Feature{{
		FeaturePlan: mpf("feature:x@plan:test@0"),
		Interval:    "@daily",
		Currency:    "usd",
	}}, pushLogger(t))
	err := c.Schedule(ctx, "org:example", info, []Phase{{
		Features: []refs.FeaturePlan{mpf("feature:x@plan:test@0")},
	}})
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	err = c.ScheduleNow(ctx, "org:example", info, nil) // org update only
	if err != nil {
		t.Fatal(err)
	}
}

func TestScheduleMinMaxItems(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	var fs []Feature
	for i := 0; i < 21; i++ {
		fs = append(fs, Feature{
			FeaturePlan: mpf(fmt.Sprintf("feature:%d@plan:test@0", i)),
			Interval:    "@daily",
			Currency:    "usd",
		})
	}

	c.Push(ctx, fs, pushLogger(t))

	err := c.SubscribeTo(ctx, "org:example", nil)
	if !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("got %v, want %v", err, ErrTooManyItems)
	}

	fps := FeaturePlans(fs)
	err = c.SubscribeTo(ctx, "org:example", fps)
	if !errors.Is(err, ErrTooManyItems) {
		t.Fatalf("got %v, want %v", err, ErrTooManyItems)
	}

	// check that we can still subscribe to the max number of items
	wantFeatures := fps[:20]
	if err := c.SubscribeTo(ctx, "org:example", wantFeatures); err != nil {
		t.Fatal(err)
	}
	got, err := c.LookupPhases(ctx, "org:example")
	if err != nil {
		t.Fatal(err)
	}

	want := []Phase{{
		Org:      "org:example",
		Features: wantFeatures,
		Current:  true,
		Plans:    nil, // fragments only
	}}
	diff.Test(t, t.Errorf, got, want, diff.ZeroFields[Phase]("Effective"))
}

func TestLookupPhasesWithTiersRoundTrip(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	fs := []Feature{
		{
			// TODO(bmizerany): G: check/test plan name formats
			FeaturePlan: mpf("feature:10@plan:test@0"),
			Interval:    "@daily",
			Currency:    "usd",
			Tiers:       []Tier{{Upto: 10}},
			Mode:        "graduated",
			Aggregate:   "sum",
		},
		{
			FeaturePlan: mpf("feature:inf@plan:test@0"),
			Interval:    "@daily",
			Currency:    "usd",
			Tiers:       []Tier{{}},
			Mode:        "graduated",
			Aggregate:   "sum",
		},
		{
			FeaturePlan: mpf("feature:lic@plan:test@0"),
			Interval:    "@daily",
			Currency:    "usd",
		},
	}

	fps := make([]refs.FeaturePlan, len(fs))
	for i, f := range fs {
		fps[i] = f.FeaturePlan
	}

	c.setClock(t, t0)
	c.Push(ctx, fs, pushLogger(t))
	if err := c.SubscribeTo(ctx, "org:example", fps); err != nil {
		t.Fatal(err)
	}

	got, err := c.LookupPhases(ctx, "org:example")
	if err != nil {
		t.Fatal(err)
	}

	want := []Phase{{
		Org:       "org:example",
		Effective: t0,
		Current:   true,
		Features:  fps,

		Plans: plans("plan:test@0"),
	}}

	diff.Test(t, t.Errorf, got, want, ignoreProviderIDs)
}

func TestSubscribeToPlan(t *testing.T) {
	fs := []Feature{{
		FeaturePlan: mpf("feature:x@plan:pro@0"),
		Interval:    "@monthly",
		Base:        100,
		Currency:    "usd",
	}, {
		FeaturePlan: mpf("feature:y@plan:pro@0"),
		Interval:    "@monthly",
		Base:        1000,
		Currency:    "usd",
	}}

	ctx := context.Background()
	tc := newTestClient(t)
	tc.Push(ctx, fs, pushLogger(t))
	tc.setClock(t, t0)

	efs, err := Expand(fs, "plan:pro@0")
	if err != nil {
		t.Fatal(err)
	}
	if err := tc.SubscribeTo(ctx, "org:example", efs); err != nil {
		t.Fatal(err)
	}

	got, err := tc.LookupPhases(ctx, "org:example")
	if err != nil {
		t.Fatal(err)
	}
	want := []Phase{{
		Org:       "org:example",
		Current:   true,
		Effective: t0,
		Features:  FeaturePlans(fs),

		Plans: plans("plan:pro@0"),
	}}

	diff.Test(t, t.Errorf, got, want, ignoreProviderIDs)
}

func TestDedupCustomer(t *testing.T) {
	fs := []Feature{{
		FeaturePlan: mpf("feature:x@plan:test@0"),
		Interval:    "@daily",
		Currency:    "usd",
	}}

	tc := newTestClient(t)
	ctx := context.Background()
	tc.Push(ctx, fs, pushLogger(t))

	var g errgroup.Group
	for i := 0; i < 3; i++ {
		g.Go(func() error {
			return tc.SubscribeTo(ctx, "org:example", FeaturePlans(fs))
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	got, err := tc.ListOrgs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Org{{ID: "org:example"}}
	diff.Test(t, t.Errorf, got, want, ignoreProviderIDs)
}

func TestLookupPhases(t *testing.T) {
	fs0 := []Feature{
		{
			FeaturePlan: mpf("feature:x@plan:test@0"),
			Interval:    "@daily",
			Currency:    "usd",
		},
		{
			FeaturePlan: mpf("feature:y@plan:test@0"),
			Interval:    "@daily",
			Currency:    "usd",
		},
	}

	tc := newTestClient(t)
	ctx := context.Background()
	tc.Push(ctx, fs0, pushLogger(t))

	tc.setClock(t, t0)

	if err := tc.SubscribeTo(ctx, "org:example", FeaturePlans(fs0)); err != nil {
		t.Fatal(err)
	}

	got, err := tc.LookupPhases(ctx, "org:example")
	if err != nil {
		t.Fatal(err)
	}
	want := []Phase{{
		Org:       "org:example",
		Current:   true,
		Effective: t0,
		Features:  FeaturePlans(fs0),

		Plans: plans("plan:test@0"),
	}}
	diff.Test(t, t.Errorf, got, want, ignoreProviderIDs)

	fs1 := []Feature{
		{
			FeaturePlan: mpf("feature:x@plan:test@1"),
			Interval:    "@daily",
			Currency:    "usd",
		},
		{
			FeaturePlan: mpf("feature:y@plan:test@1"),
			Interval:    "@daily",
			Currency:    "usd",
		},
	}
	tc.Push(ctx, fs1, pushLogger(t))

	fpsFrag := FeaturePlans(append(fs0, fs1[1:]...))
	if err := tc.SubscribeTo(ctx, "org:example", fpsFrag); err != nil {
		t.Fatal(err)
	}

	got, err = tc.LookupPhases(ctx, "org:example")
	if err != nil {
		t.Fatal(err)
	}

	for i, p := range got {
		p.Features = slices.Clone(p.Features)
		refs.SortGroupedByVersion(p.Features)
		got[i] = p
	}

	t.Logf("got: %# v", pretty.Formatter(got))

	want = []Phase{{
		Org:       "org:example",
		Current:   true,
		Effective: t0,
		Features:  fpsFrag,

		Plans: plans("plan:test@0"),
	}}

	diff.Test(t, t.Errorf, got, want, ignoreProviderIDs)
}

func TestReportUsage(t *testing.T) {
	fs := []Feature{
		{
			FeaturePlan: mpf("feature:10@plan:test@0"),
			Interval:    "@monthly",
			Currency:    "usd",
			Tiers:       []Tier{{Upto: 10}},
			Mode:        "graduated",
			Aggregate:   "sum",
		},
		{
			FeaturePlan: mpf("feature:inf@plan:test@0"),
			Interval:    "@monthly",
			Currency:    "usd",
			Tiers:       []Tier{{Upto: Inf}},
			Mode:        "graduated",
			Aggregate:   "sum",
		},
		{
			FeaturePlan: mpf("feature:lic@plan:test@0"),
			Interval:    "@monthly",
			Currency:    "usd",
		},
	}

	tc := newTestClient(t)
	ctx := context.Background()
	tc.Push(ctx, fs, pushLogger(t))
	tc.setClock(t, t0)

	if err := tc.SubscribeTo(ctx, "org:example", FeaturePlans(fs)); err != nil {
		t.Fatal(err)
	}

	g, groupCtx := errgroup.WithContext(ctx)
	report := func(feature string, n int) {
		fn, err := refs.ParseName(feature)
		if err != nil {
			t.Fatal(err)
		}
		g.Go(func() (err error) {
			defer errorfmt.Handlef("%s: %w", feature, &err)
			return tc.ReportUsage(groupCtx, "org:example", fn, Report{
				N:  n,
				At: t0,
			})
		})
	}

	report("feature:10", 3)
	report("feature:inf", 9)
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	got, err := tc.LookupLimits(ctx, "org:example")
	if err != nil {
		t.Fatal(err)
	}

	slices.SortFunc(got, func(a, b Usage) bool {
		return refs.ByName(a.Feature, b.Feature)
	})

	want := []Usage{
		{Feature: mpf("feature:10@plan:test@0"), Start: t0, End: endOfStripeMonth(t0), Used: 3, Limit: 10},
		{Feature: mpf("feature:inf@plan:test@0"), Start: t0, End: endOfStripeMonth(t0), Used: 9, Limit: Inf},
		{Feature: mpf("feature:lic@plan:test@0"), Start: t1, End: t2, Used: 1, Limit: Inf},
	}

	diff.Test(t, t.Errorf, got, want)
}

func TestReportUsageFeatureNotFound(t *testing.T) {
	tc := newTestClient(t)
	ctx := context.Background()

	fs := []Feature{{
		FeaturePlan: mpf("feature:inf@plan:test@0"),
		Interval:    "@monthly",
		Currency:    "usd",
		Tiers:       []Tier{{Upto: Inf}},
		Mode:        "graduated",
		Aggregate:   "sum",
	}}

	tc.Push(ctx, fs, pushLogger(t))
	if err := tc.SubscribeTo(ctx, "org:example", FeaturePlans(fs)); err != nil {
		t.Fatal(err)
	}
	fn := mpn("feature:nope")
	got := tc.ReportUsage(ctx, "org:example", fn, Report{})
	if !errors.Is(got, ErrFeatureNotFound) {
		t.Fatalf("got %v, want %v", got, ErrFeatureNotFound)
	}
}

func TestSubscribeToUnknownFeatures(t *testing.T) {
	tc := newTestClient(t)
	ctx := context.Background()

	fs := refs.MustParseFeaturePlans(
		"feature:A@plan:a@0",
		"feature:B@plan:b@0",
	)

	got := tc.SubscribeTo(ctx, "org:example", fs)
	if !errors.Is(got, ErrFeatureNotFound) {
		t.Fatalf("got %v, want %v", got, ErrFeatureNotFound)
	}

	// make only plan:a valid
	tc.Push(ctx, []Feature{{
		FeaturePlan: mpf("feature:A@plan:a@0"),
		Interval:    "@monthly",
		Currency:    "usd",
		Tiers:       []Tier{{Upto: Inf}},
		Mode:        "graduated",
		Aggregate:   "sum",
	}}, pushLogger(t))

	got = tc.SubscribeTo(ctx, "org:example", fs)
	if !errors.Is(got, ErrFeatureNotFound) {
		t.Fatalf("got %v, want %v", got, ErrFeatureNotFound)
	}
}

func TestSchedulePutCustomer(t *testing.T) {
	// TODO(bmizerany): convert this all to table tests
	tc := newTestClient(t)
	ctx := context.Background()

	type o = OrgInfo
	copyOrg := new(o)

	check := func(org string, in, want *OrgInfo, wantPutErr, wantLookupErr error) {
		t.Helper()
		err := tc.PutCustomer(ctx, org, in)
		if !errors.Is(err, wantPutErr) {
			t.Fatalf("got %v, want %v", err, wantPutErr)
		}
		got, err := tc.LookupOrg(ctx, org)
		if !errors.Is(err, wantLookupErr) {
			t.Fatalf("got %v, want %v", err, wantLookupErr)
		}
		if want == copyOrg {
			want = in
			if want.Metadata == nil {
				want.Metadata = map[string]string{}
			}
		}
		diff.Test(t, t.Errorf, got, want)
	}

	check("org:invalid", &o{Email: "invalid"}, nil, ErrInvalidEmail, ErrOrgNotFound)

	check("org:a", &o{Email: "a@a.com"}, copyOrg, nil, nil)
	check("org:b", &o{Email: "b@b.com"}, copyOrg, nil, nil)
	check("org:a", &o{Email: "aa@aa.com"}, copyOrg, nil, nil)

	c0 := &o{
		Email: "c@c.com",
		Metadata: map[string]string{
			"foo": "bar",
		},
	}

	check("org:c", c0, copyOrg, nil, nil)
	check("org:c", &o{
		Email: "do@notUpdate.com",
		Metadata: map[string]string{
			"foo":      "XXXX",
			"tier.baz": "qux", // should cause aborted update
		},
	}, c0, ErrInvalidMetadata, nil)

	check("org:c", &o{
		Email: "c1@c.com",
		Metadata: map[string]string{
			"foo": "bar",
			"c":   "ccc",
		},
	}, copyOrg, nil, nil)

	check("org:c", &o{
		Email: "c1@c.com",
		Metadata: map[string]string{
			"foo": "",
		},
	}, &o{
		Email: "c1@c.com",
		Metadata: map[string]string{
			// "foo" is removed
			"c": "ccc",
		},
	}, nil, nil)

	check("org:c", &o{
		Email:       "c1@c.com",
		Name:        "The Name",
		Description: "The Desc",
		Phone:       "111-111-1111",
		Metadata: map[string]string{
			"c": "ccc",
		},
	}, &o{
		Email:       "c1@c.com",
		Name:        "The Name",
		Description: "The Desc",
		Phone:       "111-111-1111",
		Metadata: map[string]string{
			"c": "ccc",
		},
	}, nil, nil)

	check("org:c", &o{
		// nothing to update; nop
	}, &o{
		Email:       "c1@c.com",
		Name:        "The Name",
		Description: "The Desc",
		Phone:       "111-111-1111",
		Metadata: map[string]string{
			"c": "ccc",
		},
	}, nil, nil)
}

func ciOnly(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("not in CI; skipping long test")
	}
}

func endOfStripeMonth(t time.Time) time.Time {
	return t.AddDate(0, 1, 0).Truncate(time.Minute).Add(-5 * time.Minute)
}

func plans(ss ...string) []refs.Plan {
	var ps []refs.Plan
	for _, s := range ss {
		ps = append(ps, mpp(s))
	}
	return ps
}
