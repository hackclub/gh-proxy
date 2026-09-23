package server

import (
	"testing"
	"time"
)

func TestStatsWriterRecordAggregates(t *testing.T) {
	w := newStatsWriter(nil)
	w.record("a", "GET", "/gh/x", 200, true)
	w.record("a", "GET", "/gh/y", 200, false)
	w.record("b", "POST", "/gh/graphql", 502, false)

	d := w.pending
	if len(d.logs) != 3 {
		t.Fatalf("logs = %d, want 3", len(d.logs))
	}
	var total, cached, perMinute int64
	for _, c := range d.days {
		total += c.total
		cached += c.cached
	}
	for _, n := range d.minutes {
		perMinute += n
	}
	if total != 3 || cached != 1 || perMinute != 3 {
		t.Errorf("days total=%d cached=%d minutes=%d, want 3/1/3", total, cached, perMinute)
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
		w.record("a", "GET", "/gh/x", 200, false)
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
	older.minutes[early] = 2
	older.keys["a"] = &keyUsage{total: 2, cached: 1, lastUsed: early}

	newer := newStatsDelta()
	newer.logs = []logRow{{path: "/2"}}
	newer.days["2026-01-01"] = &reqCount{total: 3}
	newer.days["2026-01-02"] = &reqCount{total: 1}
	newer.minutes[early] = 1
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
	if older.minutes[early] != 3 {
		t.Errorf("minute count = %d, want 3", older.minutes[early])
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
	w.record("a", "GET", "/gh/x", 200, false)
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
