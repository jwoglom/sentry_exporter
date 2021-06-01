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
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/prometheus/common/log"
	"github.com/prometheus/common/model"
)

func requestIssueCountAboveThreshold(thresh int, statsPeriod string, config HTTPProbe, client *http.Client, w http.ResponseWriter) {
	issuesList := make(map[string]int)
	extra := ""
	total := 0

	var newIssuesList map[string]int
	var err error
	for {
		log.Infof("Querying issues list with cursor '%s'", extra)
		newIssuesList, extra, err = getIssuesListByFreq(thresh, statsPeriod, extra, config, client)
		if err != nil {
			log.Error(err)
			break
		}
		for project, _ := range newIssuesList {
			issuesList[project] += newIssuesList[project]
			total += newIssuesList[project]
		}
		if extra == "" {
			break
		}
	}

	for project, _ := range issuesList {
		fmt.Fprintf(w, "sentry_project_high_freq_issues{project=\""+project+"\",above=\"%d\",period=\""+statsPeriod+"\"} %d.0\n", thresh, issuesList[project])
	}

	fmt.Fprintf(w, "sentry_high_freq_issues{above=\"%d\",period=\""+statsPeriod+"\"} %d.0\n", thresh, total)
}

func getIssuesListByFreq(thresh int, statsPeriod, extra string, config HTTPProbe, client *http.Client) (map[string]int, string, error) {
	countPerProject := make(map[string]int)
	nextExtra := ""

	url := "organizations/" + config.Organization + "/issues/?collapse=stats&expand=owners&expand=inbox&limit=25&query=is%3Aunresolved&sort=freq&statsPeriod=" + statsPeriod + "&" + extra
	resp, err := requestSentry(url, config, client)
	if err != nil {
		log.Error(err)
		return countPerProject, nextExtra, err
	}
	issues, err := extractIssues(resp.Body)
	if err != nil {
		log.Error(err)
		return countPerProject, nextExtra, err
	}
	issueIdToProject := make(map[string]string)
	var allIds []string
	for _, issue := range issues {
		issueIdToProject[issue.Id] = issue.Project.Slug
		allIds = append(allIds, issue.Id)
	}

	countPerIssueIds, err := getIssueCountForIds(allIds, statsPeriod, config, client)
	if err != nil {
		log.Error(err)
		return countPerProject, nextExtra, err
	}

	more := true
	for id, _ := range countPerIssueIds {
		if projectId, ok := issueIdToProject[id]; ok {
			if countPerIssueIds[id] >= thresh {
				countPerProject[projectId]++
			} else {
				more = false
			}
		}
	}

	if more {
		nextExtra, err = getSentryNextCursor(resp)
		if err != nil {
			log.Error(err)
		}
	}

	return countPerProject, nextExtra, nil
}

type IssuesResponse struct {
	Id      string        `json:"id"`
	Project IssuesProject `json:"project"`
}

type IssuesProject struct {
	Id   string `json:"id"`
	Slug string `json:"slug"`
}

func extractIssues(reader io.Reader) ([]IssuesResponse, error) {
	var keys []IssuesResponse
	body, err := ioutil.ReadAll(reader)
	if err != nil {
		return keys, err
	}
	err = json.Unmarshal([]byte(body), &keys)
	if err != nil {
		return keys, err
	}
	return keys, nil
}

type IssuesStatsResponse struct {
	IssuesStats
	Id       string      `json:"id"`
	Lifetime IssuesStats `json:"lifetime"`
}

type IssuesStats struct {
	Count     string `json:"count"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

func getIssueCountForIds(ids []string, statsPeriod string, config HTTPProbe, client *http.Client) (map[string]int, error) {
	ret := make(map[string]int)
	query := ""
	for _, id := range ids {
		query += fmt.Sprintf("&groups=%s", id)
	}
	url := "organizations/" + config.Organization + "/issues-stats/?query=is:unresolved&sort=freq&statsPeriod=" + statsPeriod + query
	resp, err := requestSentry(url, config, client)
	if err != nil {
		return ret, err
	}

	var data []IssuesStatsResponse
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return ret, err
	}
	err = json.Unmarshal([]byte(body), &data)
	if err != nil {
		return ret, err
	}
	if len(data) == 0 {
		return ret, nil
	}

	for _, s := range data {
		ret[s.Id], _ = strconv.Atoi(s.Count)
	}

	return ret, nil
}

func clientWithTimeout(values url.Values, timeout time.Duration) *http.Client {
	c := &http.Client{
		Timeout: timeout,
	}

	if timeout := values.Get("timeout"); timeout != "" {
		if d, err := model.ParseDuration(timeout); err != nil {
			c.Timeout = time.Duration(d)
		}
	}

	return c
}

// probeHTTPIssues writes Prometheus metrics on the number of issues which trigger
// at high frequency
func probeHTTPIssues(values url.Values, w http.ResponseWriter, module Module) bool {
	config := module.HTTP
	client := clientWithTimeout(values, module.HTTP.Issues.Timeout)

	above := 10000
	if a := config.Issues.Above; a > 0 {
		above = a
	}
	if a := values.Get("above"); a != "" {
		above, _ = strconv.Atoi(a)
	}

	period := "14d"
	if p := module.HTTP.Issues.Period; p != "" {
		period = p
	}
	if p := values.Get("period"); p != "" {
		period = p
	}
	if period != "14d" && period != "24h" {
		log.Error("Invalid period (must be 14d or 24h)")
		return false
	}

	log.Infof("Processing issues probe for period %s above %d\n", period, above)

	requestIssueCountAboveThreshold(above, period, config, client, w)

	log.Infof("Processed issues probe\n")

	return true
}
