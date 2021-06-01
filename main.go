// Copyright 2019 Paolo Garri.
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
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sync"

	"gopkg.in/yaml.v2"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
)

type Config struct {
	Modules map[string]Module `yaml:"modules"`
}

type SafeConfig struct {
	sync.RWMutex
	C *Config
}

type Module struct {
	HTTP HTTPProbe `yaml:"http"`
}

type HTTPProbe struct {
	// Defaults to 2xx.
	ValidStatusCodes []int             `yaml:"valid_status_codes"`
	Domain           string            `yaml:"domain"`
	Organization     string            `yaml:"organization"`
	RateLimit        bool              `yaml:"ratelimit"`
	Headers          map[string]string `yaml:"headers"`
	Issues           IssuesOptions     `yaml:"issues"`
	Lag              LagOptions        `yaml:"lag"`
}

type IssuesOptions struct {
	Timeout time.Duration `yaml:"timeout"`
	Period  string        `yaml:"period"`
	Above   int           `yaml:"above"`
}

type LagOptions struct {
	Timeout time.Duration `yaml:"timeout"`
}

var Probers = map[string]func(url.Values, http.ResponseWriter, Module) bool{
	"lag":    probeHTTPLag,
	"issues": probeHTTPIssues,
}

func (sc *SafeConfig) reloadConfig(confFile string) (err error) {
	var c = &Config{}

	yamlFile, err := ioutil.ReadFile(confFile)
	if err != nil {
		log.Errorf("Error reading config file: %s", err)
		return err
	}

	if err := yaml.Unmarshal(yamlFile, c); err != nil {
		log.Errorf("Error parsing config file: %s", err)
		return err
	}

	sc.Lock()
	sc.C = c
	sc.Unlock()

	log.Infoln("Loaded config file")
	return nil
}

func probeHandler(w http.ResponseWriter, r *http.Request, conf *Config) {
	params := r.URL.Query()

	moduleName := params.Get("module")
	if moduleName == "" {
		moduleName = "sentry"
	}
	module, ok := conf.Modules[moduleName]
	if !ok {
		http.Error(w, fmt.Sprintf("Unknown module %q", moduleName), 400)
		return
	}
	proberName := params.Get("prober")
	if moduleName == "" {
		moduleName = "http_lag"
	}
	prober, ok := Probers[proberName]
	if !ok {
		http.Error(w, fmt.Sprintf("Unknown prober %q", proberName), 400)
		return
	}

	log.Infof("Starting prober %s with params %#+v\n", proberName, params)
	start := time.Now()
	success := prober(params, w, module)
	fmt.Fprintf(w, "probe_duration_seconds %f\n", time.Since(start).Seconds())
	if success {
		fmt.Fprintln(w, "probe_success 1")
	} else {
		fmt.Fprintln(w, "probe_success 0")
	}
}

func init() {
	prometheus.MustRegister(version.NewCollector("sentry_exporter"))
}

func main() {
	var (
		configFile    = flag.String("config.file", "sentry_exporter.yml", "Sentry exporter configuration file.")
		listenAddress = flag.String("web.listen-address", ":9412", "The address to listen on for HTTP requests.")
		showVersion   = flag.Bool("version", false, "Print version information.")
		sc            = &SafeConfig{
			C: &Config{},
		}
	)
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print("sentry_exporter"))
		os.Exit(0)
	}

	log.Infoln("Starting sentry_exporter", version.Info())
	log.Infoln("Build context", version.BuildContext())

	if err := sc.reloadConfig(*configFile); err != nil {
		log.Fatalf("Error loading config: %s", err)
	}

	hup := make(chan os.Signal)
	reloadCh := make(chan chan error)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-hup:
				if err := sc.reloadConfig(*configFile); err != nil {
					log.Errorf("Error reloading config: %s", err)
				}
			case rc := <-reloadCh:
				if err := sc.reloadConfig(*configFile); err != nil {
					log.Errorf("Error reloading config: %s", err)
					rc <- err
				} else {
					rc <- nil
				}
			}
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/probe",
		func(w http.ResponseWriter, r *http.Request) {
			sc.RLock()
			c := sc.C
			sc.RUnlock()

			probeHandler(w, r, c)
		})
	http.HandleFunc("/-/reload",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				fmt.Fprintf(w, "This endpoint requires a POST request.\n")
				return
			}

			rc := make(chan error)
			reloadCh <- rc
			if err := <-rc; err != nil {
				http.Error(w, fmt.Sprintf("failed to reload config: %s", err), http.StatusInternalServerError)
			}
		})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(`<html>
            <head><title>Sentry Exporter</title></head>
            <body>
            <h1>Sentry Exporter</h1>
            <p><a href="/probe?target=apimutate">Probe specific Sentry project</a></p>
			<p><a href="/probe">Probe all Sentry projects</a></p>
            <p><a href="/metrics">Metrics</a></p>
            </body>
			</html>`))
		if err != nil {
			log.Fatal(err)
		}
	})

	log.Infoln("Listening on", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
		log.Fatalf("Error starting HTTP server: %s", err)
	}
}
