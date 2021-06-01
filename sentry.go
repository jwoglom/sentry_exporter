package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/prometheus/common/log"
)

type ProjectListResponse struct {
	Slug string
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
			log.Debugf("received %d from %s\n", resp.StatusCode, requestURL)
			return resp, nil
		}
	}
	return &http.Response{}, errors.New(fmt.Sprintf("Invalid response from Sentry API: %d", resp.StatusCode))
}

func getSentryNextCursor(resp *http.Response) (string, error) {
	link := resp.Header.Get("link")
	parts := strings.Split(link, ", ")
	for _, part := range parts {
		if strings.Contains(part, "rel=\"next\"") {
			semicolonParts := strings.Split(part, ";")
			for _, p := range semicolonParts {
				if strings.Contains(p, "cursor=\"") {
					cursor := strings.TrimSpace(p)
					cursor = strings.TrimPrefix(cursor, "cursor=\"")
					cursor = strings.TrimSuffix(cursor, "\"")
					return fmt.Sprintf("cursor=%s", cursor), nil
				}
			}
		}
	}
	return "", errors.New("unable to identify next cursor")
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

var allProjectsCache []string
var timesSinceUpdate = 0

func getOrUpdateProjectsList(config HTTPProbe, client *http.Client, failures *int, w http.ResponseWriter) []string {
	if len(allProjectsCache) == 0 || timesSinceUpdate > 50 {
		allProjectsCache = allSentryProjects(config, client, failures, w)
		timesSinceUpdate = 0
	} else {
		timesSinceUpdate++
	}

	return allProjectsCache
}
