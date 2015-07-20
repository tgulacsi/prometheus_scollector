// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"bosun.org/opentsdb"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/inconshreveable/log15.v2"
)

var (
	Log = log15.New()

	sampleExpiry  = flag.Duration("scollector.sample-expiry", 5*time.Minute, "How long a sample is valid for.")
	lastProcessed = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "scollector_last_processed_timestamp_seconds",
			Help: "Unix timestamp of the last processed scollector metric.",
		},
	)
)

// Most of the ideas are stolen from https://github.com/prometheus/graphite_exporter/blob/master/main.go
// https://github.com/prometheus/graphite_exporter/commit/298611cd340e0a34bc9d2d434f47456c6c201221

type scollectorSample struct {
	Name      string
	Labels    map[string]string
	Help      string
	Value     float64
	Type      prometheus.ValueType
	Timestamp time.Time
}

type scollectorCollector struct {
	samples map[string]scollectorSample
	types   map[string]string
	ch      chan scollectorSample
	mu      sync.Mutex
}

func newScollectorCollector() *scollectorCollector {
	c := &scollectorCollector{
		ch:      make(chan scollectorSample, 0),
		samples: make(map[string]scollectorSample, 512),
		types:   make(map[string]string, 512),
	}
	go c.processSamples()
	return c
}

func (c *scollectorCollector) processSamples() {
	ticker := time.NewTicker(time.Minute).C
	for {
		select {
		case sample := <-c.ch:
			c.mu.Lock()
			c.samples[sample.Name] = sample
			c.mu.Unlock()
		case <-ticker:
			// Garbage collect expired samples.
			ageLimit := time.Now().Add(-*sampleExpiry)
			c.mu.Lock()
			for k, sample := range c.samples {
				if ageLimit.After(sample.Timestamp) {
					delete(c.samples, k)
				}
			}
			c.mu.Unlock()
		}
	}
}

// Collect implements prometheus.Collector.
func (c *scollectorCollector) Collect(ch chan<- prometheus.Metric) {
	Log.Debug("Collect", "samples", len(c.samples))
	ch <- lastProcessed
	Log.Debug("Collect", "lastProcessed", lastProcessed)

	c.mu.Lock()
	samples := make([]scollectorSample, 0, len(c.samples))
	for _, sample := range c.samples {
		samples = append(samples, sample)
	}
	c.mu.Unlock()

	ageLimit := time.Now().Add(-*sampleExpiry)
	for _, sample := range samples {
		if ageLimit.After(sample.Timestamp) {
			Log.Debug("skipping old sample", "limit", ageLimit, "sample", sample)
			continue
		}
		Log.Debug("sending sample", "sample", sample)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(sample.Name, sample.Help, []string{}, sample.Labels),
			sample.Type,
			sample.Value,
		)
	}
}

// Describe implements prometheus.Collector.
func (c *scollectorCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- lastProcessed.Desc()
}

var dotReplacer = strings.NewReplacer(".", "_")

func (c *scollectorCollector) handleScoll(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	sendErr := func(msg string, code int) {
		Log.Error(msg)
		http.Error(w, msg, http.StatusMethodNotAllowed)
	}
	if r.Method != "POST" {
		sendErr("Only POST is allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		sendErr("Only application/json is allowed", http.StatusBadRequest)
		return
	}

	rdr := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		var err error
		if rdr, err = gzip.NewReader(rdr); err != nil {
			sendErr("Not gzipped: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	var batch []opentsdb.DataPoint
	if err := json.NewDecoder(rdr).Decode(&batch); err != nil {
		sendErr("cannot decode JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	Log.Debug("batch", "size", len(batch))
	n := 0
	for _, m := range batch {
		Log.Debug("got", "msg", m)
		name := clearName(m.Metric, true, 'x')
		if name == "" {
			Log.Warn("bad metric name: " + m.Metric)
			continue
		}

		var v float64
		switch x := m.Value.(type) {
		case float64:
			v = x
		case int:
			v = float64(x)
		case int64:
			v = float64(x)
		case int32:
			v = float64(x)
		case string: // type info
			if x != "" {
				if z, ok := c.types[name]; !ok || z != x {
					c.types[name] = x
				}
			}
			continue
		default:
			Log.Warn("unknown value", "type", fmt.Sprintf("%T", m.Value), "msg", m)
			continue
		}
		typ := prometheus.GaugeValue
		if c.types[name] == "counter" {
			typ = prometheus.CounterValue
		}
		var ts time.Time
		if m.Timestamp >= 1e10 {
			ts = time.Unix(
				m.Timestamp&(1e10-1),
				int64(math.Mod(float64(m.Timestamp), 1e10))*int64(time.Millisecond))
		} else {
			ts = time.Unix(m.Timestamp, 0)
		}
		for k := range m.Tags {
			k2 := clearName(k, false, 'x')
			if k2 != "" && k2 != k {
				Log.Warn("bad label name " + k)
				m.Tags[k2] = m.Tags[k]
				delete(m.Tags, k)
			}
		}
		if m.Tags["host"] != "" {
			m.Tags["instance"] = m.Tags["host"]
			delete(m.Tags, "host")
		}
		c.ch <- scollectorSample{
			Name:      name,
			Labels:    m.Tags,
			Type:      typ,
			Help:      fmt.Sprintf("Scollector metric %s (%s)", m.Metric, c.types[name]),
			Value:     v,
			Timestamp: ts,
		}
		n++
	}
	lastProcessed.Set(float64(time.Now().UnixNano()) / 1e9)
	Log.Info("processed", "messages", n, "samples", len(c.samples), "types", len(c.types))

	w.WriteHeader(http.StatusNoContent)
}

func main() {
	Log.SetHandler(log15.CallerFileHandler(log15.StderrHandler))

	flagAddr := flag.String("http", "0.0.0.0:9107", "address to listen on")
	flagScollPref := flag.String("scollector.prefix", "/api/put", "HTTP path prefix for listening for scollector-sent data")
	flagVerbose := flag.Bool("v", false, "verbose logging")

	flag.Parse()
	if !*flagVerbose {
		Log.SetHandler(log15.LvlFilterHandler(log15.LvlInfo, log15.StderrHandler))
	}

	c := newScollectorCollector()
	prometheus.MustRegister(c)

	http.HandleFunc(*flagScollPref, c.handleScoll)
	http.Handle("/metrics", prometheus.Handler())
	Log.Info("Serving on " + *flagAddr)
	http.ListenAndServe(*flagAddr, nil)
}

// clearName replaces non-allowed characters.
func clearName(txt string, allowColon bool, replaceRune rune) string {
	if replaceRune == 0 {
		replaceRune = -1
	}
	i := -1
	return strings.Map(
		func(r rune) rune {
			i++
			if r == '.' {
				return '_'
			}
			if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || r == '_' {
				return r
			}
			if allowColon && r == ':' {
				return r
			}
			if i > 0 && '0' <= r && r <= '9' {
				return r
			}
			return replaceRune
		},
		txt)
}
