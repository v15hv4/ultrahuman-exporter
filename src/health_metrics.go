package main

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type metricMapping struct {
	Name        string
	Description string
	Unit        string
	Kind        string
}

func buildHealthMetrics(resp apiResponse) ([]metricdata.Metrics, int) {
	seriesPoints := map[string][]metricdata.DataPoint[float64]{}
	sumPoints := map[string][]metricdata.DataPoint[float64]{}

	for date, metrics := range resp.Data.Metrics {
		for _, metric := range metrics {
			mapping, ok := healthMetricMappings[metric.Type]
			if !ok {
				continue
			}

			if len(metric.Object.Values) > 0 {
				for _, value := range metric.Object.Values {
					ts := time.Unix(value.Timestamp, 0)
					point := metricdata.DataPoint[float64]{Attributes: dateAttr(date), Time: ts, Value: value.Value}
					if mapping.Kind == "sum" {
						sumPoints[metric.Type] = append(sumPoints[metric.Type], point)
					} else {
						seriesPoints[metric.Type] = append(seriesPoints[metric.Type], point)
					}
				}
				continue
			}

			if metric.Object.Value == nil {
				continue
			}

			ts := scalarTimestamp(date, metric.Object.DayStartTimestamp)
			seriesPoints[metric.Type] = append(seriesPoints[metric.Type], metricdata.DataPoint[float64]{
				Attributes: dateAttr(date),
				Time:       ts,
				Value:      *metric.Object.Value,
			})
		}
	}

	result := make([]metricdata.Metrics, 0, len(seriesPoints)+len(sumPoints))
	exported := 0
	for apiType, points := range seriesPoints {
		mapping := healthMetricMappings[apiType]
		result = append(result, metricdata.Metrics{
			Name:        mapping.Name,
			Description: mapping.Description,
			Unit:        mapping.Unit,
			Data:        metricdata.Gauge[float64]{DataPoints: points},
		})
		exported += len(points)
	}
	for apiType, points := range sumPoints {
		mapping := healthMetricMappings[apiType]
		result = append(result, metricdata.Metrics{
			Name:        mapping.Name,
			Description: mapping.Description,
			Unit:        mapping.Unit,
			Data: metricdata.Sum[float64]{
				DataPoints:  points,
				Temporality: metricdata.CumulativeTemporality,
				IsMonotonic: true,
			},
		})
		exported += len(points)
	}

	return result, exported
}

func scalarTimestamp(date string, dayStart int64) time.Time {
	if dayStart > 0 {
		return time.Unix(dayStart, 0)
	}

	parsed, err := time.Parse("2006-01-02", date)
	if err == nil {
		return parsed
	}

	return time.Now()
}

func dateAttr(date string) attribute.Set {
	if date == "" {
		return attribute.Set{}
	}
	return attribute.NewSet(attribute.String("date", date))
}

var healthMetricMappings = map[string]metricMapping{
	"hr":                {Name: "uh_heart_rate_bpm", Description: "Heart rate", Unit: "{beats}/min"},
	"temp":              {Name: "uh_skin_temperature_celsius", Description: "Skin temperature", Unit: "Cel"},
	"spo2":              {Name: "uh_spo2_percent", Description: "Blood oxygen saturation", Unit: "%"},
	"hrv":               {Name: "uh_hrv_ms", Description: "Heart rate variability", Unit: "ms"},
	"steps":             {Name: "uh_steps_total", Description: "Steps", Unit: "{steps}", Kind: "sum"},
	"sleep_rhr":         {Name: "uh_resting_heart_rate_bpm", Description: "Resting heart rate", Unit: "{beats}/min"},
	"avg_sleep_hrv":     {Name: "uh_avg_sleep_hrv_ms", Description: "Average sleep heart rate variability", Unit: "ms"},
	"recovery_index":    {Name: "uh_recovery_index", Description: "Recovery index", Unit: "1"},
	"movement_index":    {Name: "uh_movement_index", Description: "Movement index", Unit: "1"},
	"active_minutes":    {Name: "uh_active_minutes", Description: "Active minutes", Unit: "min"},
	"movements":         {Name: "uh_movements_count", Description: "Movements", Unit: "{movements}"},
	"morning_alertness": {Name: "uh_morning_alertness", Description: "Morning alertness", Unit: "1"},
	"deep_sleep":        {Name: "uh_deep_sleep_minutes", Description: "Deep sleep", Unit: "min"},
	"light_sleep":       {Name: "uh_light_sleep_minutes", Description: "Light sleep", Unit: "min"},
	"rem_sleep":         {Name: "uh_rem_sleep_minutes", Description: "REM sleep", Unit: "min"},
	"total_sleep":       {Name: "uh_total_sleep_minutes", Description: "Total sleep", Unit: "min"},
	"sleep_efic":        {Name: "uh_sleep_efficiency_percent", Description: "Sleep efficiency", Unit: "%"},
}
