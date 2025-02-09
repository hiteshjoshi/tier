// Package tier contains the local client for interacting with the tier sidecar
// API.
//
// For more information, please see https://tier.run/docs.
package tier

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"tier.run/api/apitypes"
	"tier.run/fetch"
	"tier.run/refs"
)

const Inf = 1<<63 - 1

type Client struct {
	HTTPClient *http.Client
	sidecar    string
}

func NewTierSidecarClient(sidecarBase string) *Client {
	return &Client{
		HTTPClient: http.DefaultClient,
		sidecar:    sidecarBase,
	}
}

func (c *Client) client() *http.Client {
	if c.HTTPClient == nil {
		return http.DefaultClient
	}
	return c.HTTPClient
}

// Push pushes the provided pricing model to Stripe.
func (c *Client) Push(ctx context.Context, m apitypes.Model) (apitypes.PushResponse, error) {
	return fetch.OK[apitypes.PushResponse, *apitypes.Error](ctx, c.client(), "POST", c.sidecar+"/v1/push", m)
}

func (c *Client) PushJSON(ctx context.Context, m []byte) (apitypes.PushResponse, error) {
	return fetch.OK[apitypes.PushResponse, *apitypes.Error](ctx, c.client(), "POST", c.sidecar+"/v1/push", json.RawMessage(m))
}

// Pull fetches the complete pricing model from Stripe.
func (c *Client) Pull(ctx context.Context) (apitypes.Model, error) {
	return fetch.OK[apitypes.Model, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/pull", nil)
}

// PullJSON fetches the complete pricing model from Stripe and returns the raw
// JSON response.
func (c *Client) PullJSON(ctx context.Context) ([]byte, error) {
	return fetch.OK[[]byte, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/pull", nil)
}

// WhoIS reports the Stripe ID for the given organization.
func (c *Client) WhoIs(ctx context.Context, org string) (apitypes.WhoIsResponse, error) {
	return fetch.OK[apitypes.WhoIsResponse, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/whois?org="+org, nil)
}

// LookupOrg reports all known information about the provided org. The
// information is not cached by the server. If only the Stripe customer ID is
// needed and speed is of concern, users should use WhoIs.
func (c *Client) LookupOrg(ctx context.Context, org string) (apitypes.WhoIsResponse, error) {
	return fetch.OK[apitypes.WhoIsResponse, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/whois?include=info&org="+org, nil)
}

// LookupPhase reports information about the current phase the provided org is scheduled in.
func (c *Client) LookupPhase(ctx context.Context, org string) (apitypes.PhaseResponse, error) {
	return fetch.OK[apitypes.PhaseResponse, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/phase?org="+org, nil)
}

// LookupLimits reports the current usage and limits for the provided org.
func (c *Client) LookupLimits(ctx context.Context, org string) (apitypes.UsageResponse, error) {
	return fetch.OK[apitypes.UsageResponse, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/limits?org="+org, nil)
}

// LookupLimit reports the current usage and limits for the provided org and
// feature. If the feature is not currently available to the org, both limit
// and used are zero and no error is reported.
//
// It reports an error if any.
func (c *Client) LookupLimit(ctx context.Context, org, feature string) (limit, used int, err error) {
	fn, err := refs.ParseName(feature)
	if err != nil {
		return 0, 0, err
	}
	limits, err := c.LookupLimits(ctx, org)
	if err != nil {
		return 0, 0, err
	}
	for _, u := range limits.Usage {
		if u.Feature == fn {
			return u.Limit, u.Used, nil
		}
	}
	return 0, 0, nil
}

// An Answer is the response to any question for Can. It can be used in a few
// forms to shorten the logic necessary to know if a program should proceed to
// perform a user request based on their entitlements.
type Answer struct {
	ok     bool
	err    error
	report func(n int) error
}

// OK reports if the program should proceed with a user request or not. To
// prevent total failure if Can needed to reach the sidecar and was unable to,
// OK will fail optimistically and report true. If the opposite is desired,
// clients can check Err.
func (c Answer) OK() bool { return c.ok }

// Err returns the error, if any, that occurred during the call to Can.
func (c Answer) Err() error { return c.err }

// Report is the same as calling ReportN(1).
func (c Answer) Report() error { return c.ReportN(1) }

// ReportN reports usage of n units for the feature and org provided to Can.
func (c Answer) ReportN(n int) error {
	if c.report != nil {
		return c.report(n)
	}
	return nil
}

// Can is a convenience function for checking if an org has used more of a
// feature than they are entitled to and optionally reporting usage post check
// and consumption.
//
// If reporting consumption is not required, it can be used in the form:
//
//	if c.Can(ctx, "org:acme", "feature:convert").OK() { ... }
//
// reporting usage post consumption looks like:
//
//	ans := c.Can(ctx, "org:acme", "feature:convert")
//	if !ans.OK() {
//	  return ""
//	}
//	defer ans.Report() // or ReportN
//	return convert(temp)
func (c *Client) Can(ctx context.Context, org, feature string) Answer {
	limit, used, err := c.LookupLimit(ctx, org, feature)
	if err != nil {
		// TODO(bmizerany): caching of usage and limits in imminent and
		// the cache can be consulted before failing to "allow by
		// default", but for now simply allow by default right away.
		return Answer{ok: true, err: err}
	}
	if used >= limit {
		return Answer{}
	}
	report := func(n int) error {
		return c.Report(ctx, org, feature, 1)
	}
	return Answer{ok: true, report: report}
}

// Report reports a usage of n for the provided org and feature at the current
// time.
func (c *Client) Report(ctx context.Context, org, feature string, n int) error {
	fn, err := refs.ParseName(feature)
	if err != nil {
		return err
	}
	_, err = fetch.OK[struct{}, *apitypes.Error](ctx, c.client(), "POST", c.sidecar+"/v1/report", apitypes.ReportRequest{
		Org:     org,
		Feature: fn,
		N:       n,
		At:      time.Now(),
	})
	return err
}

// ReportUsage reports usage based on the provided ReportRequest fields.
func (c *Client) ReportUsage(ctx context.Context, r apitypes.ReportRequest) error {
	_, err := fetch.OK[struct{}, *apitypes.Error](ctx, c.client(), "POST", c.sidecar+"/v1/report", r)
	return err
}

// Subscribe subscribes the provided org to the provided feature or plan,
// effective immediately.
//
// Any in-progress scheduled is overwritten and the customer is billed with
// prorations immediately.
func (c *Client) Subscribe(ctx context.Context, org string, featuresAndPlans ...string) error {
	_, err := fetch.OK[struct{}, *apitypes.Error](ctx, c.client(), "POST", c.sidecar+"/v1/subscribe", apitypes.ScheduleRequest{
		Org:    org,
		Phases: []apitypes.Phase{{Features: featuresAndPlans}},
	})
	return err
}

type Phase = apitypes.Phase
type OrgInfo = apitypes.OrgInfo

type ScheduleParams struct {
	Info   *OrgInfo
	Phases []Phase
}

func (c *Client) Schedule(ctx context.Context, org string, p *ScheduleParams) error {
	_, err := fetch.OK[struct{}, *apitypes.Error](ctx, c.client(), "POST", c.sidecar+"/v1/subscribe", &apitypes.ScheduleRequest{
		Org:    org,
		Info:   (*apitypes.OrgInfo)(p.Info),
		Phases: copyPhases(p.Phases),
	})
	return err
}

func copyPhases(phases []Phase) []apitypes.Phase {
	c := make([]apitypes.Phase, len(phases))
	for i, p := range phases {
		c[i] = apitypes.Phase(p)
	}
	return c
}

func (c *Client) WhoAmI(ctx context.Context) (apitypes.WhoAmIResponse, error) {
	return fetch.OK[apitypes.WhoAmIResponse, *apitypes.Error](ctx, c.client(), "GET", c.sidecar+"/v1/whoami", nil)
}
