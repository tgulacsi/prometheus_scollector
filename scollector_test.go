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
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tgulacsi/go/loghlp/tsthlp"
)

func TestScoll(t *testing.T) {
	Log.SetHandler(tsthlp.TestHandler(t))
	c := newScollectorCollector()
	prometheus.MustRegister(c)

	http.HandleFunc(*flagScollPref, c.handleScoll)
	http.Handle("/metrics", prometheus.Handler())
	Log.Info("Serving on " + *flagAddr)
	go http.ListenAndServe(*flagAddr, nil)

	port := strings.SplitN(*flagAddr, ":", 2)[1]
	cmd := exec.Command("scollector", "-h=http://localhost:"+port)
	cmd.Stderr = os.Stderr
	t.Logf("starting scollector")
	if err := cmd.Start(); err != nil {
		t.Skipf("Cannot start scollector: %v", err)
		return
	}
	var body bytes.Buffer
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		resp, err := http.Get("http://" + *flagAddr + "/metrics")
		if err != nil {
			t.Errorf("cannot get /metrics: %v", err)
			t.FailNow()
		}
		body.Reset()
		_, err = io.Copy(&body, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Logf("Response:\n%s", body.Bytes())
			t.Errorf("Error reading response body: %v", err)
			continue
		}
		if bytes.Contains(body.Bytes(), []byte("\nos_cpu{")) {
			//t.Logf("Response:\n%s", body.Bytes())
			break
		}
	}
	_ = cmd.Process.Kill()

	var lines []string
	for _, line := range bytes.Split(body.Bytes(), []byte("\n")) {
		if bytes.HasPrefix(line, []byte("os_disk_fs_percent_free{")) {
			lines = append(lines, string(line))
		}
	}
	if len(lines) < 2 {
		t.Errorf("awaited at least 2 lines of \"os_disk_fs_percent_free\", got %d: %q",
			len(lines), lines)
	}
}
