package load

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stats is the on-stage readout. The split between float_limit_exceeded and
// upstream_unavailable is the whole point: the first is the system working correctly
// under load, the second is the system failing. They must never be reported as one
// number.
type Stats struct {
	Attempted atomic.Int64
	Created   atomic.Int64
	Replayed  atomic.Int64

	FloatLimitExceeded  atomic.Int64
	UpstreamUnavailable atomic.Int64

	CallbacksSent   atomic.Int64
	CallbacksFailed atomic.Int64

	TransportErrors atomic.Int64

	latencyTotalNS atomic.Int64
	latencyMaxNS   atomic.Int64

	mu         sync.Mutex
	otherCodes map[string]int64
}

func NewStats() *Stats { return &Stats{otherCodes: map[string]int64{}} }

func (s *Stats) observeLatency(d time.Duration) {
	s.latencyTotalNS.Add(int64(d))
	for {
		cur := s.latencyMaxNS.Load()
		if int64(d) <= cur || s.latencyMaxNS.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

// otherCode records any outcome that is not one of the named ones, keyed by the API's
// stable machine-readable code (README: "Errors over HTTP") so the tail is diagnosable
// rather than an undifferentiated "errors" bucket.
func (s *Stats) otherCode(code string) {
	s.mu.Lock()
	s.otherCodes[code]++
	s.mu.Unlock()
}

func (s *Stats) othersSummary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.otherCodes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.otherCodes))
	for k := range s.otherCodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, s.otherCodes[k]))
	}
	return strings.Join(parts, " ")
}

type snapshot struct {
	attempted, created, replayed  int64
	floatLimit, upstreamUnavail   int64
	transportErrors, callbackFail int64
	latencyTotalNS                int64
}

func (s *Stats) snapshot() snapshot {
	return snapshot{
		attempted:       s.Attempted.Load(),
		created:         s.Created.Load(),
		replayed:        s.Replayed.Load(),
		floatLimit:      s.FloatLimitExceeded.Load(),
		upstreamUnavail: s.UpstreamUnavailable.Load(),
		transportErrors: s.TransportErrors.Load(),
		callbackFail:    s.CallbacksFailed.Load(),
		latencyTotalNS:  s.latencyTotalNS.Load(),
	}
}

// Line renders one interval. 503s are printed even when zero, so the moment they start
// climbing is visible against a steady column rather than appearing from nowhere.
func (s *Stats) Line(now time.Time, prev snapshot, elapsed time.Duration) (string, snapshot) {
	cur := s.snapshot()
	d := func(a, b int64) int64 { return a - b }

	sent := d(cur.attempted, prev.attempted)
	perSec := float64(sent) / elapsed.Seconds()

	avg := time.Duration(0)
	if n := d(cur.attempted, prev.attempted); n > 0 {
		avg = time.Duration(d(cur.latencyTotalNS, prev.latencyTotalNS) / n)
	}

	line := fmt.Sprintf(
		"%s  sent %5d (%6.1f/s)  ok %5d  replay %4d  409 float %5d  503 upstream %5d  net-err %4d  cb-fail %4d  avg %6s",
		now.Format("15:04:05"),
		sent, perSec,
		d(cur.created, prev.created),
		d(cur.replayed, prev.replayed),
		d(cur.floatLimit, prev.floatLimit),
		d(cur.upstreamUnavail, prev.upstreamUnavail),
		d(cur.transportErrors, prev.transportErrors),
		d(cur.callbackFail, prev.callbackFail),
		avg.Round(time.Millisecond),
	)
	if other := s.othersSummary(); other != "" {
		line += "  [" + other + "]"
	}
	return line, cur
}

func (s *Stats) Final(total time.Duration) string {
	cur := s.snapshot()
	avg := time.Duration(0)
	if cur.attempted > 0 {
		avg = time.Duration(cur.latencyTotalNS / cur.attempted)
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "\n--- loadgen summary over %s ---\n", total.Round(time.Second))
	fmt.Fprintf(b, "  attempted                  %d\n", cur.attempted)
	fmt.Fprintf(b, "  created (201)              %d\n", cur.created)
	fmt.Fprintf(b, "  idempotent replay (200)    %d\n", cur.replayed)
	fmt.Fprintf(b, "  409 float_limit_exceeded   %d\n", cur.floatLimit)
	fmt.Fprintf(b, "  503 upstream_unavailable   %d\n", cur.upstreamUnavail)
	fmt.Fprintf(b, "  transport errors           %d\n", cur.transportErrors)
	fmt.Fprintf(b, "  callbacks sent / failed    %d / %d\n", s.CallbacksSent.Load(), cur.callbackFail)
	if other := s.othersSummary(); other != "" {
		fmt.Fprintf(b, "  other response codes       %s\n", other)
	}
	fmt.Fprintf(b, "  latency avg / max          %s / %s\n",
		avg.Round(time.Millisecond),
		time.Duration(s.latencyMaxNS.Load()).Round(time.Millisecond))
	return b.String()
}
