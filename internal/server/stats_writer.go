package server

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxBufferedLogs matches what LogsJanitor retains, so buffering more than
// this would only write rows that are deleted seconds later.
const maxBufferedLogs = 1000

// statsWriter buffers request logs and usage counters in memory and writes
// them in one batch per interval. Updating system_stats and
// request_stats_hourly per request made every request queue on the same row
// lock; flushing sums once a second turns that into one update per interval.
type statsWriter struct {
	pool    *pgxpool.Pool
	loc     *time.Location // day boundary for today_requests
	mu      sync.Mutex
	pending statsDelta
}

type statsDelta struct {
	logs    []logRow
	days    map[string]*reqCount // America/New_York date -> counts
	minutes map[time.Time]int64  // request minute -> count, bucketed by hour in SQL
	keys    map[string]*keyUsage // api key hash -> usage
}

type logRow struct {
	apiKey, method, path string
	status               int32
	hit                  bool
	at                   time.Time
}

type reqCount struct{ total, cached int64 }

type keyUsage struct {
	total, cached int64
	lastUsed      time.Time
}

func newStatsWriter(pool *pgxpool.Pool) *statsWriter {
	// Use NYC Eastern Time for daily reset as requested
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return &statsWriter{pool: pool, loc: loc, pending: newStatsDelta()}
}

func newStatsDelta() statsDelta {
	return statsDelta{
		days:    make(map[string]*reqCount),
		minutes: make(map[time.Time]int64),
		keys:    make(map[string]*keyUsage),
	}
}

// record adds one request to the pending batch; it never touches the database.
func (w *statsWriter) record(apiKeyHash, method, path string, status int, hit bool) {
	now := time.Now()
	day := now.In(w.loc).Format("2006-01-02")
	w.mu.Lock()
	defer w.mu.Unlock()
	d := &w.pending
	d.logs = append(d.logs, logRow{apiKey: apiKeyHash, method: method, path: path, status: int32(status), hit: hit, at: now})
	d.trimLogs()
	c := d.days[day]
	if c == nil {
		c = &reqCount{}
		d.days[day] = c
	}
	c.total++
	// Minutes rather than hours so Postgres's date_trunc('hour') stays exact
	// in time zones with half-hour offsets.
	d.minutes[now.Truncate(time.Minute)]++
	k := d.keys[apiKeyHash]
	if k == nil {
		k = &keyUsage{}
		d.keys[apiKeyHash] = k
	}
	k.total++
	k.lastUsed = now
	if hit {
		c.cached++
		k.cached++
	}
}

// run flushes every interval until ctx is canceled.
func (w *statsWriter) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := w.flush(fctx); err != nil {
				log.Printf("stats flush failed, will retry: %v", err)
			}
			cancel()
		}
	}
}

// flush writes everything recorded so far. On failure the counts go back into
// the pending batch so the next flush retries them.
func (w *statsWriter) flush(ctx context.Context) error {
	w.mu.Lock()
	d := w.pending
	w.pending = newStatsDelta()
	w.mu.Unlock()
	if d.empty() {
		return nil
	}
	// The batch runs as one implicit transaction, so a failure applies none of it.
	if err := w.pool.SendBatch(ctx, d.batch()).Close(); err != nil {
		w.mu.Lock()
		d.merge(w.pending)
		w.pending = d
		w.mu.Unlock()
		return err
	}
	return nil
}

func (d *statsDelta) empty() bool {
	return len(d.logs) == 0 && len(d.days) == 0 && len(d.minutes) == 0 && len(d.keys) == 0
}

func (d *statsDelta) trimLogs() {
	if n := len(d.logs) - maxBufferedLogs; n > 0 {
		d.logs = append(d.logs[:0], d.logs[n:]...)
	}
}

// merge folds newer into d, keeping log rows in request order.
func (d *statsDelta) merge(newer statsDelta) {
	d.logs = append(d.logs, newer.logs...)
	d.trimLogs()
	for day, c := range newer.days {
		if cur := d.days[day]; cur != nil {
			cur.total += c.total
			cur.cached += c.cached
		} else {
			d.days[day] = c
		}
	}
	for m, n := range newer.minutes {
		d.minutes[m] += n
	}
	for hash, k := range newer.keys {
		if cur := d.keys[hash]; cur != nil {
			cur.total += k.total
			cur.cached += k.cached
			if k.lastUsed.After(cur.lastUsed) {
				cur.lastUsed = k.lastUsed
			}
		} else {
			d.keys[hash] = k
		}
	}
}

// batch builds one statement per table, in a fixed table order so concurrent
// flushes from several instances take row locks in the same order.
func (d *statsDelta) batch() *pgx.Batch {
	b := &pgx.Batch{}

	if len(d.logs) > 0 {
		keys := make([]string, len(d.logs))
		methods := make([]string, len(d.logs))
		paths := make([]string, len(d.logs))
		statuses := make([]int32, len(d.logs))
		hits := make([]bool, len(d.logs))
		ats := make([]time.Time, len(d.logs))
		for i, l := range d.logs {
			keys[i], methods[i], paths[i], statuses[i], hits[i], ats[i] = l.apiKey, l.method, l.path, l.status, l.hit, l.at
		}
		b.Queue(`
			INSERT INTO request_logs (api_key, method, path, status, cache_hit, created_at)
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::int[], $5::bool[], $6::timestamptz[])
		`, keys, methods, paths, statuses, hits, ats)
	}

	// Oldest day first. A flush carrying an older day than the row already
	// has (another instance crossed midnight first) only adds to the totals.
	days := make([]string, 0, len(d.days))
	for day := range d.days {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		c := d.days[day]
		b.Queue(`
			INSERT INTO system_stats (id, total_requests, total_cached_requests, today_requests, today_date)
			VALUES (1, $1, $2, $1, $3::date)
			ON CONFLICT (id) DO UPDATE SET
				total_requests = system_stats.total_requests + $1,
				total_cached_requests = system_stats.total_cached_requests + $2,
				today_requests = CASE
					WHEN system_stats.today_date = $3::date THEN system_stats.today_requests + $1
					WHEN system_stats.today_date > $3::date THEN system_stats.today_requests
					ELSE $1
				END,
				today_date = GREATEST(system_stats.today_date, $3::date),
				updated_at = now()
		`, c.total, c.cached, day)
	}

	if len(d.minutes) > 0 {
		mins := make([]time.Time, 0, len(d.minutes))
		counts := make([]int64, 0, len(d.minutes))
		for m, n := range d.minutes {
			mins = append(mins, m)
			counts = append(counts, n)
		}
		b.Queue(`
			INSERT INTO request_stats_hourly (hour, requests)
			SELECT date_trunc('hour', m), SUM(n) FROM unnest($1::timestamptz[], $2::bigint[]) AS v(m, n)
			GROUP BY 1
			ON CONFLICT (hour) DO UPDATE SET requests = request_stats_hourly.requests + EXCLUDED.requests
		`, mins, counts)
	}

	if len(d.keys) > 0 {
		hashes := make([]string, 0, len(d.keys))
		for hash := range d.keys {
			hashes = append(hashes, hash)
		}
		sort.Strings(hashes)
		totals := make([]int64, len(hashes))
		cached := make([]int64, len(hashes))
		lastUsed := make([]time.Time, len(hashes))
		for i, hash := range hashes {
			k := d.keys[hash]
			totals[i], cached[i], lastUsed[i] = k.total, k.cached, k.lastUsed
		}
		b.Queue(`
			UPDATE api_keys k SET
				total_requests = k.total_requests + v.total,
				total_cached_requests = k.total_cached_requests + v.cached,
				last_used_at = GREATEST(k.last_used_at, v.last_used)
			FROM unnest($1::text[], $2::bigint[], $3::bigint[], $4::timestamptz[]) AS v(key_hash, total, cached, last_used)
			WHERE k.key_hash = v.key_hash
		`, hashes, totals, cached, lastUsed)
	}
	return b
}
