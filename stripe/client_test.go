package stripe

import (
	"context"
	"net/http"
	"testing"

	"tier.run/fetch/fetchtest"
)

func newTestClient(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *Client {
	hc := fetchtest.NewTLSServer(t, h)
	c := &Client{
		BaseURL:    fetchtest.BaseURL(hc),
		HTTPClient: hc,
		Logf:       t.Logf,
	}
	return c
}

func TestSetIdempotencyKey(t *testing.T) {
	var got string
	c := newTestClient(t, func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Idempotency-Key")
	})

	var f Form
	f.SetIdempotencyKey("foo")
	if err := c.Do(context.Background(), "POST", "/", f, nil); err != nil {
		t.Fatal(err)
	}
	if got != "foo" {
		t.Errorf("got %q; want %q", got, "foo")
	}
}

func TestInvalidAPIKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"message": "Invalid API Key provided: foo"}}`))
	})

	var f Form
	if err := c.Do(context.Background(), "POST", "/", f, nil); err != ErrInvalidAPIKey {
		t.Errorf("got %v; want %v", err, ErrInvalidAPIKey)
	}
}
