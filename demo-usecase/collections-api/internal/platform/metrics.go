package platform

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// A minimal Prometheus-text-format registry.
//
// README.md says to pick the smallest reasonable dependency set - standard library plus pgx,
// a router and a structured logger. That rules out the Prometheus client library, and the
// surface we actually need (OBS-2 request/error/duration, OBS-3 per-run, OBS-4 settlement
// lag) is small enough to carry ourselves.

type Labels map[string]string

func (l Labels) key() string {
	if len(l) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for k := range l {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(l[n])
	}
	return b.String()
}

func (l Labels) render() string {
	if len(l) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for k := range l {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(l[n])
		parts = append(parts, fmt.Sprintf("%s=%q", n, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

type metricKind string

const (
	kindCounter   metricKind = "counter"
	kindGauge     metricKind = "gauge"
	kindHistogram metricKind = "histogram"
)

type sample struct {
	labels  Labels
	value   float64
	buckets []uint64 // histogram only
	sum     float64
	count   uint64
}

type family struct {
	name    string
	help    string
	kind    metricKind
	buckets []float64
	series  map[string]*sample
	order   []string
}

type Registry struct {
	mu       sync.Mutex
	fams     map[string]*family
	famOrder []string
	// constLabels go onto every series - environment, per OBS-1/OBS-2.
	constLabels Labels
}

func NewRegistry(constLabels Labels) *Registry {
	return &Registry{fams: map[string]*family{}, constLabels: constLabels}
}

func (r *Registry) family(name, help string, kind metricKind, buckets []float64) *family {
	f, ok := r.fams[name]
	if !ok {
		f = &family{name: name, help: help, kind: kind, buckets: buckets, series: map[string]*sample{}}
		r.fams[name] = f
		r.famOrder = append(r.famOrder, name)
	}
	return f
}

func (r *Registry) sampleFor(f *family, labels Labels) *sample {
	merged := Labels{}
	for k, v := range r.constLabels {
		merged[k] = v
	}
	for k, v := range labels {
		merged[k] = v
	}
	k := merged.key()
	s, ok := f.series[k]
	if !ok {
		s = &sample{labels: merged}
		if f.kind == kindHistogram {
			s.buckets = make([]uint64, len(f.buckets))
		}
		f.series[k] = s
		f.order = append(f.order, k)
	}
	return s
}

// IncCounter adds 1 to a counter series.
func (r *Registry) IncCounter(name, help string, labels Labels) {
	r.AddCounter(name, help, labels, 1)
}

func (r *Registry) AddCounter(name, help string, labels Labels, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sampleFor(r.family(name, help, kindCounter, nil), labels).value += delta
}

// SetGauge replaces a gauge value. OBS-4's settlement lag is a gauge.
func (r *Registry) SetGauge(name, help string, labels Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sampleFor(r.family(name, help, kindGauge, nil), labels).value = v
}

// DefaultDurationBuckets are seconds, sized for an HTTP path where NFR-1 wants p99 < 400ms.
var DefaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.4, 1, 2.5, 10}

func (r *Registry) ObserveHistogram(name, help string, buckets []float64, labels Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.family(name, help, kindHistogram, buckets)
	s := r.sampleFor(f, labels)
	for i, b := range f.buckets {
		if v <= b {
			s.buckets[i]++
		}
	}
	s.sum += v
	s.count++
}

// Handler serves the Prometheus text exposition format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(r.Render()))
	})
}

func (r *Registry) Render() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	for _, fname := range r.famOrder {
		f := r.fams[fname]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		for _, sk := range f.order {
			s := f.series[sk]
			switch f.kind {
			case kindHistogram:
				// s.buckets is already cumulative: ObserveHistogram increments every
				// bucket whose bound the value falls under.
				for i, bound := range f.buckets {
					l := Labels{}
					for k, v := range s.labels {
						l[k] = v
					}
					l["le"] = strconv.FormatFloat(bound, 'g', -1, 64)
					fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, l.render(), s.buckets[i])
				}
				inf := Labels{}
				for k, v := range s.labels {
					inf[k] = v
				}
				inf["le"] = "+Inf"
				fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, inf.render(), s.count)
				fmt.Fprintf(&b, "%s_sum%s %s\n", f.name, s.labels.render(), strconv.FormatFloat(s.sum, 'g', -1, 64))
				fmt.Fprintf(&b, "%s_count%s %d\n", f.name, s.labels.render(), s.count)
			default:
				fmt.Fprintf(&b, "%s%s %s\n", f.name, s.labels.render(), strconv.FormatFloat(s.value, 'g', -1, 64))
			}
		}
	}
	return b.String()
}
