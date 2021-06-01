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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/common/log"
)

type RateLimitResponse struct {
	Window int
	Count  int
}

type ProjectKeyResponse struct {
	ID        string
	Name      string
	Label     string
	RateLimit *RateLimitResponse
}

type ProjectListResponse struct {
	Slug string
}

func extractRateLimit(reader io.Reader) (float64, error) {
	var keys []ProjectKeyResponse
	body, err := ioutil.ReadAll(reader)
	if err != nil {
		return 0, err
	}
	err = json.Unmarshal([]byte(body), &keys)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 || keys[0].RateLimit == nil {
		return 0, nil
	}
	return float64(keys[0].RateLimit.Count) / float64(keys[0].RateLimit.Window), nil
}

func extractErrorRate(reader io.Reader) (int, int, error) {
	var stats [][]int
	count := 0
	latestTimestamp := 0
	body, err := ioutil.ReadAll(reader)
	if err != nil {
		return 0, 0, err
	}
	err = json.Unmarshal([]byte(body), &stats)
	if err != nil {
		return 0, 0, err
	}
	// ignore the last timestamp
	for i := len(stats) - 1; i >= 0; i-- {
		ts := stats[i][0]
		c := stats[i][1]
		count = count + c
		if latestTimestamp == 0 && c > 0 {
			latestTimestamp = ts
		}
	}
	return count, latestTimestamp, nil
}

func extractSentryProjects(reader io.Reader) ([]string, error) {
	var resp []ProjectListResponse
	var projects []string
	body, err := ioutil.ReadAll(reader)
	if err != nil {
		return projects, err
	}
	err = json.Unmarshal([]byte(body), &resp)
	if err != nil {
		return projects, err
	}
	for _, p := range resp {
		projects = append(projects, p.Slug)
	}
	return projects, nil
}

func requestSentry(path string, config HTTPProbe, client *http.Client) (*http.Response, error) {
	requestURL := config.Domain + "/api/0/" + path

	request, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		log.Errorf("Error creating request for target %s: %s", path, err)
		return &http.Response{}, err
	}

	for key, value := range config.Headers {
		if strings.Title(key) == "Host" {
			request.Host = value
			continue
		}
		request.Header.Set(key, value)
	}

	resp, err := client.Do(request)
	// Err won't be nil if redirects were turned off. See https://github.com/golang/go/issues/3795
	if err != nil {
		log.Warnf("Error for HTTP request to %s: %s", path, err)
	} else {
		if len(config.ValidStatusCodes) != 0 {
			for _, code := range config.ValidStatusCodes {
				if resp.StatusCode == code {
					return resp, nil
				}
			}
		} else if 200 <= resp.StatusCode && resp.StatusCode < 300 {
			log.Infof("received %d from %s\n", resp.StatusCode, requestURL)
			return resp, nil
		}
	}
	return &http.Response{}, errors.New("Invalid response from Sentry API")
}

func generateLag(latestTimestamp int) int64 {
	return time.Now().Unix() - int64(latestTimestamp)
}

func requestEventCount(target string, stat string, config HTTPProbe, client *http.Client, w http.ResponseWriter) (int, error) {
	// Get the last hour stats
	var lastMin = strconv.FormatInt(time.Now().Unix()-60*60, 10)
	resp, err := requestSentry("projects/"+config.Organization+"/"+target+"/stats/?resolution=10s&stat="+stat+"&since="+lastMin, config, client)

	var rate int
	var latestTimestamp int
	if err == nil {
		defer resp.Body.Close()
		rate, latestTimestamp, err = extractErrorRate(resp.Body)
		lag := generateLag(latestTimestamp)
		fmt.Fprintf(w, "sentry_events_total{stat=\""+stat+"\",project=\""+target+"\"} %d\n", rate)
		if latestTimestamp > 0 {
			fmt.Fprintf(w, "sentry_project_latest_timestamp{stat=\""+stat+"\",project=\""+target+"\"} %d\n", latestTimestamp)
			fmt.Fprintf(w, "sentry_project_lag_seconds{stat=\""+stat+"\",project=\""+target+"\"} %d\n", lag)
		}
	} else {
		log.Error(err)
	}
	return latestTimestamp, err
}

func requestRateLimit(target string, config HTTPProbe, client *http.Client, w http.ResponseWriter) error {
	resp, err := requestSentry("projects/"+config.Organization+"/"+target+"/keys/", config, client)

	var rate float64
	if err == nil {
		defer resp.Body.Close()
		rate, err = extractRateLimit(resp.Body)
		fmt.Fprintf(w, "sentry_project_rate_limit_seconds_total{project=\""+target+"\"} %f\n", rate)
	} else {
		log.Error(err)
	}
	return err
}

func allSentryProjects(config HTTPProbe, client *http.Client, failures *int, w http.ResponseWriter) []string {
	resp, err := requestSentry("organizations/"+config.Organization+"/projects/", config, client)

	var projects []string
	if err == nil {
		projects, err = extractSentryProjects(resp.Body)
		fmt.Fprintf(w, "sentry_projects_total %d\n", len(projects))
	} else {
		log.Error(err)
		*failures++
	}

	return projects
}

func probeProject(target string, config HTTPProbe, client *http.Client, w http.ResponseWriter, failures *int, lastTsChan chan<- int, wg *sync.WaitGroup) {
	defer wg.Done()
	var err error
	var latestTimestamp int

	latestTimestamp, err = requestEventCount(target, "received", config, client, w)
	if err != nil {
		*failures++
	}
	_, err = requestEventCount(target, "rejected", config, client, w)
	if err != nil {
		*failures++
	}
	if config.RateLimit {
		err = requestRateLimit(target, config, client, w)
		if err != nil {
			*failures++
		}
	}
	log.Infof("Processed project %s\n", target)
	lastTsChan <- latestTimestamp
}

var allProjectsCache []string
var timesSinceUpdate = 0

func probeHTTP(target string, w http.ResponseWriter, module Module) bool {
	config := module.HTTP

	client := &http.Client{
		Timeout: module.Timeout,
	}

	failures := 0

	var targets []string
	if target != "" {
		targets = append(targets, target)
	} else if len(allProjectsCache) == 0 || timesSinceUpdate > 50 {
		targets = allSentryProjects(config, client, &failures, w)
		timesSinceUpdate = 0
	} else {
		targets = allProjectsCache
		timesSinceUpdate++
	}
	log.Infof("Processing probe for %d Sentry projects\n", len(targets))

	var wg sync.WaitGroup
	ch := make(chan int, len(targets))
	for _, t := range targets {
		wg.Add(1)
		go probeProject(t, config, client, w, &failures, ch, &wg)
		time.Sleep(50 * time.Millisecond)
	}

	latestTimestamp := -1
	for range targets {
		ts := <-ch
		if ts == 0 {
			continue
		}
		if latestTimestamp == -1 || ts > latestTimestamp {
			latestTimestamp = ts
		}
	}

	wg.Wait()
	if latestTimestamp > -1 {
		fmt.Fprintf(w, "sentry_events_latest_timestamp %d\n", latestTimestamp)
		fmt.Fprintf(w, "sentry_events_lag_seconds %d\n", generateLag(latestTimestamp))
	}
	fmt.Fprintf(w, "sentry_fetch_failures %d\n", failures)

	return true
}
