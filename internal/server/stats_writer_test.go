package server

import (
	"testing"
	"time"
)

func TestStatsWriterRecordAggregates(t *testing.T) {
	w := newStatsWriter(nil)
	w.record("a", "GET", "/gh/x", 200, true, 2*time.Millisecond)
	w.record("a", "GET", "/gh/y", 200, false, 300*time.Millisecond)
	w.record("b", "POST", "/gh/graphql", 502, false, 10*time.Second)

	d := w.pending
	if len(d.logs) != 3 {
		t.Fatalf("logs = %d, want 3", len(d.logs))
	}
	var total, cached, perMinute, latencyUs int64
	for _, c := range d.days {
		total += c.total
		cached += c.cached
	}
	for _, m := range d.minutes {
		perMinute += m.requests
		latencyUs += m.latencyUs
	}
	if total != 3 || cached != 1 || perMinute != 3 {
		t.Errorf("days total=%d cached=%d minutes=%d, want 3/1/3", total, cached, perMinute)
	}
	if latencyUs != 10_302_000 {
		t.Errorf("latency sum = %dus, want 10302000", latencyUs)
	}
	if k := d.keys["a"]; k == nil || k.total != 2 || k.cached != 1 {
		t.Errorf("key a = %+v, want total 2 cached 1", k)
	}
	if k := d.keys["b"]; k == nil || k.total != 1 || k.cached != 0 {
		t.Errorf("key b = %+v, want total 1 cached 0", k)
	}
}

func TestStatsWriterCapsBufferedLogs(t *testing.T) {
	w := newStatsWriter(nil)
	for i := 0; i < maxBufferedLogs+50; i++ {
		w.record("a", "GET", "/gh/x", 200, false, time.Millisecond)
	}
	if n := len(w.pending.logs); n != maxBufferedLogs {
		t.Errorf("logs = %d, want %d", n, maxBufferedLogs)
	}
	// Counters are not capped, only the log rows.
	if k := w.pending.keys["a"]; k.total != maxBufferedLogs+50 {
		t.Errorf("key total = %d, want %d", k.total, maxBufferedLogs+50)
	}
}

func TestStatsDeltaMergeKeepsOrderAndSums(t *testing.T) {
	early := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)

	older := newStatsDelta()
	older.logs = []logRow{{path: "/1"}}
	older.days["2026-01-01"] = &reqCount{total: 2, cached: 1}
	older.minutes[early] = &minuteCount{requests: 2, latencyUs: 500}
	older.keys["a"] = &keyUsage{total: 2, cached: 1, lastUsed: early}

	newer := newStatsDelta()
	newer.logs = []logRow{{path: "/2"}}
	newer.days["2026-01-01"] = &reqCount{total: 3}
	newer.days["2026-01-02"] = &reqCount{total: 1}
	newer.minutes[early] = &minuteCount{requests: 1, latencyUs: 250}
	newer.keys["a"] = &keyUsage{total: 1, lastUsed: late}

	older.merge(newer)
	if older.logs[0].path != "/1" || older.logs[1].path != "/2" {
		t.Errorf("log order = %v", older.logs)
	}
	if c := older.days["2026-01-01"]; c.total != 5 || c.cached != 1 {
		t.Errorf("day 1 = %+v, want total 5 cached 1", c)
	}
	if c := older.days["2026-01-02"]; c == nil || c.total != 1 {
		t.Errorf("day 2 = %+v, want total 1", c)
	}
	if m := older.minutes[early]; m.requests != 3 || m.latencyUs != 750 {
		t.Errorf("minute = %+v, want 3 requests 750us", m)
	}
	if k := older.keys["a"]; k.total != 3 || !k.lastUsed.Equal(late) {
		t.Errorf("key a = %+v, want total 3 lastUsed %v", k, late)
	}
}

func TestStatsDeltaBatchSkipsEmptyTables(t *testing.T) {
	d := newStatsDelta()
	if !d.empty() {
		t.Fatal("new delta should be empty")
	}
	if n := d.batch().Len(); n != 0 {
		t.Errorf("empty batch has %d statements", n)
	}
	w := newStatsWriter(nil)
	w.record("a", "GET", "/gh/x", 200, false, time.Millisecond)
	// logs, system_stats (one day), hourly, api_keys
	if n := w.pending.batch().Len(); n != 4 {
		t.Errorf("batch has %d statements, want 4", n)
	}
}

func TestAPIKeyCache(t *testing.T) {
	c := newAPIKeyCache(50 * time.Millisecond)
	if _, ok := c.get("h"); ok {
		t.Fatal("empty cache returned a hit")
	}
	c.put("h", apiKeyInfo{perSec: 7, display: "d"})
	if info, ok := c.get("h"); !ok || info.perSec != 7 || info.display != "d" {
		t.Fatalf("get = %+v, %v", info, ok)
	}
	c.forget("h")
	if _, ok := c.get("h"); ok {
		t.Error("forgotten key still cached")
	}
	c.put("h", apiKeyInfo{perSec: 7})
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.get("h"); ok {
		t.Error("expired key still cached")
	}
}

func TestFormatLatency(t *testing.T) {
	tests := []struct {
		sumUs, samples int64
		want           string
	}{
		{0, 0, "—"},
		{840, 1, "0.84 ms"},
		{24_600, 2, "12.3 ms"},
		{240_000, 1, "240 ms"},
		{3_040_000, 2, "1.52 s"},
	}
	for _, tt := range tests {
		if got := formatLatency(tt.sumUs, tt.samples); got != tt.want {
			t.Errorf("formatLatency(%d, %d) = %q, want %q", tt.sumUs, tt.samples, got, tt.want)
		}
	}
}
