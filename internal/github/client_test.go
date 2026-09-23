package github

import (
	"net/http"
	"testing"
	"time"
)

func rateHeaders(limit, remaining, reset, resource string) http.Header {
	h := http.Header{}
	h.Set("X-RateLimit-Limit", limit)
	h.Set("X-RateLimit-Remaining", remaining)
	h.Set("X-RateLimit-Reset", reset)
	if resource != "" {
		h.Set("X-RateLimit-Resource", resource)
	}
	return h
}

func TestRecordRateHeaders(t *testing.T) {
	c := New(nil)
	if c.recordRateHeaders("t1", "core", http.Header{}) {
		t.Fatal("missing headers should report false")
	}
	if !c.recordRateHeaders("t1", "core", rateHeaders("5000", "4990", "1700000000", "")) {
		t.Fatal("valid headers should report true")
	}
	// X-RateLimit-Resource overrides the category guessed from the URL.
	c.recordRateHeaders("t1", "core", rateHeaders("30", "29", "1700000000", "search"))

	got := c.rates[rateKey{"t1", "core"}]
	if got.Limit != 5000 || got.Remaining != 4990 || !got.Reset.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("core = %+v", got)
	}
	if got := c.rates[rateKey{"t1", "search"}]; got.Remaining != 29 {
		t.Errorf("search = %+v", got)
	}
}

func TestRecordRateHeadersKeepsNewest(t *testing.T) {
	c := New(nil)
	k := rateKey{"t1", "core"}
	c.recordRateHeaders("t1", "core", rateHeaders("5000", "4000", "1000", "core"))
	// A slower response from earlier in the same window arrives late.
	c.recordRateHeaders("t1", "core", rateHeaders("5000", "4100", "1000", "core"))
	if r := c.rates[k].Remaining; r != 4000 {
		t.Errorf("remaining = %d, want 4000 (stale sample ignored)", r)
	}
	// The window reset: a later reset wins even with more remaining.
	c.recordRateHeaders("t1", "core", rateHeaders("5000", "4999", "4600", "core"))
	if r := c.rates[k].Remaining; r != 4999 {
		t.Errorf("remaining = %d, want 4999 after reset", r)
	}
	// A response from the previous window is ignored.
	c.recordRateHeaders("t1", "core", rateHeaders("5000", "3000", "1000", "core"))
	if r := c.rates[k].Remaining; r != 4999 {
		t.Errorf("remaining = %d, want 4999", r)
	}
}
