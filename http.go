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
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/common/log"
)

func extractErrorRate(reader io.Reader, config HTTPProbe) int {
	var re = regexp.MustCompile(`(\d+)]]$`)
	body, err := ioutil.ReadAll(reader)
	if err != nil {
		log.Errorf("Error reading HTTP body: %s", err)
		return 0
	}
	var str = string(body)
	matches := re.FindStringSubmatch(str)
	value, err := strconv.Atoi(matches[1])
	if err == nil {
		return value
	}
	return 0
}

type RateLimitResponse struct {
	Window int
	Count int
}

type ProjectKeyResponse struct {
	ID string
	Name string
	Label string
	RateLimit *RateLimitResponse
}

func extractRateLimit(reader io.Reader, config HTTPProbe) int {
	var keys []ProjectKeyResponse
	body, err := ioutil.ReadAll(reader)
	if err != nil {
		return 0
	}
	err = json.Unmarshal([]byte(body), &keys)
	if err != nil || len(keys) == 0 || keys[0].RateLimit == nil {
		return 0
	}
	return keys[0].RateLimit.Count
}

func requestSentry(path string, config HTTPProbe, client *http.Client) (*http.Response, error) {
	requestURL := config.Prefix + path

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
			return resp, nil
		}
	}
	return &http.Response{}, errors.New("Invalid response from Sentry API")
}

func requestErrorReceived(target string, config HTTPProbe, client *http.Client, w http.ResponseWriter) {
	resp, err := requestSentry(target + "/stats/", config, client)

	if err == nil {
		defer resp.Body.Close()
		fmt.Fprintf(w, "probe_sentry_error_received %d\n", extractErrorRate(resp.Body, config))
	}
	return
}

func requestRateLimit(target string, config HTTPProbe, client *http.Client, w http.ResponseWriter) {
	resp, err := requestSentry(target + "/keys/", config, client)

	if err == nil {
		defer resp.Body.Close()
		fmt.Fprintf(w, "probe_sentry_rate_limit_minute %d\n", extractRateLimit(resp.Body, config))
	}
	return
}

func probeHTTP(target string, w http.ResponseWriter, module Module) (bool) {
	config := module.HTTP

	client := &http.Client{
		Timeout: module.Timeout,
	}

	requestErrorReceived(target, config, client, w)
	requestRateLimit(target, config, client, w)

	return true
}
