package index

import (
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// bucketSeconds is the granularity of usage over time. A quarter of an hour
// divides every UTC offset in use, so buckets fall into local days exactly
// whatever the time zone is when Daily runs.
const bucketSeconds = 15 * 60

// dayBuckets is usage over time: bucket start (Unix seconds) → model →
// usage. Bucket 0 holds usage from lines with no timestamp yet.
type dayBuckets map[int64]map[string]core.Usage

func (b dayBuckets) add(bucket int64, model string, u core.Usage) {
	m := b[bucket]
	if m == nil {
		m = map[string]core.Usage{}
		b[bucket] = m
	}
	m[model] = m[model].Add(u)
}

// merge adds every bucket of o to b.
func (b dayBuckets) merge(o dayBuckets) {
	for bucket, models := range o {
		for model, u := range models {
			b.add(bucket, model, u)
		}
	}
}

// dayTracker files the usage an accumulator counts under the quarter hour
// it happened in. It is the accumulator's usage hook, so deduplication
// stays the accumulator's.
type dayTracker struct {
	buckets dayBuckets
}

// newDayTracker starts from cached buckets, or from none when b is nil.
func newDayTracker(b dayBuckets) *dayTracker {
	d := &dayTracker{buckets: dayBuckets{}}
	d.buckets.merge(b)
	return d
}

// record is an Accumulator.OnUsage hook.
func (d *dayTracker) record(at time.Time, model string, u core.Usage) {
	var bucket int64
	if !at.IsZero() {
		sec := at.Unix()
		bucket = sec - sec%bucketSeconds
	}
	d.buckets.add(bucket, model, u)
}

// dayBuckets returns a new merge of the buckets of every transcript of t.
func (t *tracked) dayBuckets() dayBuckets {
	b := dayBuckets{}
	for _, f := range t.files() {
		b.merge(f.days.buckets)
	}
	return b
}

// dayKey identifies a local calendar day.
type dayKey struct {
	y int
	m time.Month
	d int
}

func keyOf(t time.Time) dayKey {
	y, m, d := t.Date()
	return dayKey{y, m, d}
}

// Daily returns usage for the last days local calendar days, oldest first,
// including days with no activity. Usage includes subagents; CostUSD is an
// estimate, 0 unless every model used that day has a price.
func (s *Store) Daily(days int) []core.DayUsage {
	if days <= 0 {
		return nil
	}
	y, m, d := time.Now().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	out := make([]core.DayUsage, days)
	index := make(map[dayKey]int, days)
	for i := range out {
		date := today.AddDate(0, 0, i-days+1)
		out[i] = core.DayUsage{Date: date, Usage: map[string]core.Usage{}}
		index[keyOf(date)] = i
	}

	s.mu.Lock()
	prices := s.prices
	touched := make([]bool, days)
	for _, p := range s.sums {
		clear(touched)
		for bucket, models := range p.days {
			if bucket == 0 {
				continue
			}
			i, ok := index[keyOf(time.Unix(bucket, 0).In(time.Local))]
			if !ok {
				continue
			}
			for model, u := range models {
				out[i].Usage[model] = out[i].Usage[model].Add(u)
			}
			touched[i] = true
		}
		for i, ok := range touched {
			if ok {
				out[i].Sessions++
			}
		}
	}
	s.mu.Unlock()

	for i := range out {
		if c, ok := prices.Estimate(out[i].Usage); ok {
			out[i].CostUSD = c
		}
	}
	return out
}
