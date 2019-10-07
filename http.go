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

func probeHTTP(target string, w http.ResponseWriter, module Module) (success bool) {
	config := module.HTTP

	client := &http.Client{
		Timeout: module.Timeout,
	}

	requestURL := config.Prefix + target + "/stats/"

	request, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		log.Errorf("Error creating request for target %s: %s", target, err)
		return
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
	if err != nil && resp == nil {
		log.Warnf("Error for HTTP request to %s: %s", target, err)
	} else {
		defer resp.Body.Close()
		if len(config.ValidStatusCodes) != 0 {
			for _, code := range config.ValidStatusCodes {
				if resp.StatusCode == code {
					success = true
					break
				}
			}
		} else if 200 <= resp.StatusCode && resp.StatusCode < 300 {
			success = true
		}
		if success {
			fmt.Fprintf(w, "probe_sentry_error_received %d\n", extractErrorRate(resp.Body, config))
		}
	}
	if resp == nil {
		resp = &http.Response{}
	}

	fmt.Fprintf(w, "probe_sentry_status_code %d\n", resp.StatusCode)
	fmt.Fprintf(w, "probe_sentry_content_length %d\n", resp.ContentLength)

	return
}
