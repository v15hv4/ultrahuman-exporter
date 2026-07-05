package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type apiResponse struct {
	Data struct {
		Metrics map[string][]apiMetric `json:"metrics"`
	} `json:"data"`
}

type apiMetric struct {
	Type   string       `json:"type"`
	Object metricObject `json:"object"`
}

type metricObject struct {
	DayStartTimestamp int64    `json:"day_start_timestamp"`
	Value             *float64 `json:"value"`
	Values            []sample `json:"values"`
}

type sample struct {
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
}

func (a *app) fetchMetrics(ctx context.Context, start, end int64) (apiResponse, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.APIURL, nil)
	if err != nil {
		return apiResponse{}, "", err
	}

	q := req.URL.Query()
	q.Set("start_epoch", strconv.FormatInt(start, 10))
	q.Set("end_epoch", strconv.FormatInt(end, 10))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", a.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		return apiResponse{}, "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return apiResponse{}, strconv.Itoa(res.StatusCode), err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiResponse{}, strconv.Itoa(res.StatusCode), fmt.Errorf("ultrahuman API returned status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload apiResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return apiResponse{}, strconv.Itoa(res.StatusCode), err
	}

	return payload, strconv.Itoa(res.StatusCode), nil
}
