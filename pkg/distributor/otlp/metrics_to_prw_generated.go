// Code generated from Prometheus sources - DO NOT EDIT.

// Copyright 2024 The Prometheus Authors
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
// Provenance-includes-location: https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/95e8f8fdc2a9dc87230406c9a3cf02be4fd68bea/pkg/translator/prometheusremotewrite/metrics_to_prw.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: Copyright The OpenTelemetry Authors.

package otlp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/multierr"

	"github.com/grafana/mimir/pkg/mimirpb"

	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/util/annotations"
)

type PromoteResourceAttributes struct {
	promoteAll bool
	attrs      map[string]struct{}
}

type Settings struct {
	Namespace                         string
	ExternalLabels                    map[string]string
	DisableTargetInfo                 bool
	ExportCreatedMetric               bool
	AddMetricSuffixes                 bool
	SendMetadata                      bool
	AllowUTF8                         bool
	PromoteResourceAttributes         *PromoteResourceAttributes
	KeepIdentifyingResourceAttributes bool
	ConvertHistogramsToNHCB           bool
	AllowDeltaTemporality             bool
	// LookbackDelta is the PromQL engine lookback delta.
	LookbackDelta time.Duration
	// PromoteScopeMetadata controls whether to promote OTel scope metadata to metric labels.
	PromoteScopeMetadata    bool
	EnableTypeAndUnitLabels bool

	// Mimir specifics.
	EnableCreatedTimestampZeroIngestion        bool
	EnableStartTimeQuietZero                   bool
	ValidIntervalCreatedTimestampZeroIngestion time.Duration
}

type StartTsAndTs struct {
	Labels  []mimirpb.LabelAdapter
	StartTs int64
	Ts      int64
}

// MimirConverter converts from OTel write format to Mimir remote write format.
type MimirConverter struct {
	unique    map[uint64]*mimirpb.TimeSeries
	conflicts map[uint64][]*mimirpb.TimeSeries
	everyN    everyNTimes
	metadata  []mimirpb.MetricMetadata
}

func NewMimirConverter() *MimirConverter {
	return &MimirConverter{
		unique:    map[uint64]*mimirpb.TimeSeries{},
		conflicts: map[uint64][]*mimirpb.TimeSeries{},
	}
}

func TranslatorMetricFromOtelMetric(metric pmetric.Metric) otlptranslator.Metric {
	m := otlptranslator.Metric{
		Name: metric.Name(),
		Unit: metric.Unit(),
		Type: otlptranslator.MetricTypeUnknown,
	}
	switch metric.Type() {
	case pmetric.MetricTypeGauge:
		m.Type = otlptranslator.MetricTypeGauge
	case pmetric.MetricTypeSum:
		if metric.Sum().IsMonotonic() {
			m.Type = otlptranslator.MetricTypeMonotonicCounter
		} else {
			m.Type = otlptranslator.MetricTypeNonMonotonicCounter
		}
	case pmetric.MetricTypeSummary:
		m.Type = otlptranslator.MetricTypeSummary
	case pmetric.MetricTypeHistogram:
		m.Type = otlptranslator.MetricTypeHistogram
	case pmetric.MetricTypeExponentialHistogram:
		m.Type = otlptranslator.MetricTypeExponentialHistogram
	}
	return m
}

type scope struct {
	name       string
	version    string
	schemaURL  string
	attributes pcommon.Map
}

func newScopeFromScopeMetrics(scopeMetrics pmetric.ScopeMetrics) scope {
	s := scopeMetrics.Scope()
	return scope{
		name:       s.Name(),
		version:    s.Version(),
		schemaURL:  scopeMetrics.SchemaUrl(),
		attributes: s.Attributes(),
	}
}

// FromMetrics converts pmetric.Metrics to Mimir remote write format.
func (c *MimirConverter) FromMetrics(ctx context.Context, md pmetric.Metrics, settings Settings, logger *slog.Logger) (annots annotations.Annotations, errs error) {
	namer := otlptranslator.MetricNamer{
		Namespace:          settings.Namespace,
		WithMetricSuffixes: settings.AddMetricSuffixes,
		UTF8Allowed:        settings.AllowUTF8,
	}
	c.everyN = everyNTimes{n: 128}
	resourceMetricsSlice := md.ResourceMetrics()

	numMetrics := 0
	for i := 0; i < resourceMetricsSlice.Len(); i++ {
		scopeMetricsSlice := resourceMetricsSlice.At(i).ScopeMetrics()
		for j := 0; j < scopeMetricsSlice.Len(); j++ {
			numMetrics += scopeMetricsSlice.At(j).Metrics().Len()
		}
	}
	c.metadata = make([]mimirpb.MetricMetadata, 0, numMetrics)

	for i := 0; i < resourceMetricsSlice.Len(); i++ {
		resourceMetrics := resourceMetricsSlice.At(i)
		resource := resourceMetrics.Resource()
		scopeMetricsSlice := resourceMetrics.ScopeMetrics()
		// keep track of the earliest and latest timestamp in the ResourceMetrics for
		// use with the "target" info metric
		earliestTimestamp := pcommon.Timestamp(math.MaxUint64)
		latestTimestamp := pcommon.Timestamp(0)
		for j := 0; j < scopeMetricsSlice.Len(); j++ {
			scopeMetrics := scopeMetricsSlice.At(j)
			scope := newScopeFromScopeMetrics(scopeMetrics)
			metricSlice := scopeMetrics.Metrics()

			// TODO: decide if instrumentation library information should be exported as labels
			for k := 0; k < metricSlice.Len(); k++ {
				if err := c.everyN.checkContext(ctx); err != nil {
					errs = multierr.Append(errs, err)
					return
				}

				metric := metricSlice.At(k)
				earliestTimestamp, latestTimestamp = findMinAndMaxTimestamps(metric, earliestTimestamp, latestTimestamp)
				temporality, hasTemporality, err := aggregationTemporality(metric)
				if err != nil {
					errs = multierr.Append(errs, err)
					continue
				}

				if hasTemporality &&
					// Cumulative temporality is always valid.
					// Delta temporality is also valid if AllowDeltaTemporality is true.
					// All other temporality values are invalid.
					(temporality != pmetric.AggregationTemporalityCumulative &&
						(!settings.AllowDeltaTemporality || temporality != pmetric.AggregationTemporalityDelta)) {
					errs = multierr.Append(errs, fmt.Errorf("invalid temporality and type combination for metric %q", metric.Name()))
					continue
				}

				metadata := mimirpb.MetricMetadata{
					Type:             otelMetricTypeToPromMetricType(metric),
					MetricFamilyName: namer.Build(TranslatorMetricFromOtelMetric(metric)),
					Help:             metric.Description(),
					Unit:             metric.Unit(),
				}
				c.metadata = append(c.metadata, metadata)

				// handle individual metrics based on type
				//exhaustive:enforce
				switch metric.Type() {
				case pmetric.MetricTypeGauge:
					dataPoints := metric.Gauge().DataPoints()
					if dataPoints.Len() == 0 {
						errs = multierr.Append(errs, fmt.Errorf("empty data points. %s is dropped", metric.Name()))
						break
					}
					if err := c.addGaugeNumberDataPoints(ctx, dataPoints, resource, settings, metadata, scope); err != nil {
						errs = multierr.Append(errs, err)
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return
						}
					}
				case pmetric.MetricTypeSum:
					dataPoints := metric.Sum().DataPoints()
					if dataPoints.Len() == 0 {
						errs = multierr.Append(errs, fmt.Errorf("empty data points. %s is dropped", metric.Name()))
						break
					}
					if err := c.addSumNumberDataPoints(ctx, dataPoints, resource, metric, settings, metadata, scope, logger); err != nil {
						errs = multierr.Append(errs, err)
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return
						}
					}
				case pmetric.MetricTypeHistogram:
					dataPoints := metric.Histogram().DataPoints()
					if dataPoints.Len() == 0 {
						errs = multierr.Append(errs, fmt.Errorf("empty data points. %s is dropped", metric.Name()))
						break
					}
					if settings.ConvertHistogramsToNHCB {
						ws, err := c.addCustomBucketsHistogramDataPoints(
							ctx, dataPoints, resource, settings, metadata, temporality, scope,
						)
						annots.Merge(ws)
						if err != nil {
							errs = multierr.Append(errs, err)
							if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
								return
							}
						}
					} else {
						if err := c.addHistogramDataPoints(ctx, dataPoints, resource, settings, metadata, scope, logger); err != nil {
							errs = multierr.Append(errs, err)
							if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
								return
							}
						}
					}
				case pmetric.MetricTypeExponentialHistogram:
					dataPoints := metric.ExponentialHistogram().DataPoints()
					if dataPoints.Len() == 0 {
						errs = multierr.Append(errs, fmt.Errorf("empty data points. %s is dropped", metric.Name()))
						break
					}
					ws, err := c.addExponentialHistogramDataPoints(
						ctx,
						dataPoints,
						resource,
						settings,
						metadata,
						temporality,
						scope,
					)
					annots.Merge(ws)
					if err != nil {
						errs = multierr.Append(errs, err)
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return
						}
					}
				case pmetric.MetricTypeSummary:
					dataPoints := metric.Summary().DataPoints()
					if dataPoints.Len() == 0 {
						errs = multierr.Append(errs, fmt.Errorf("empty data points. %s is dropped", metric.Name()))
						break
					}
					if err := c.addSummaryDataPoints(ctx, dataPoints, resource, settings, metadata, scope, logger); err != nil {
						errs = multierr.Append(errs, err)
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return
						}
					}
				default:
					errs = multierr.Append(errs, errors.New("unsupported metric type"))
				}
			}
		}
		if earliestTimestamp < pcommon.Timestamp(math.MaxUint64) {
			// We have at least one metric sample for this resource.
			// Generate a corresponding target_info series.
			addResourceTargetInfo(resource, settings, earliestTimestamp.AsTime(), latestTimestamp.AsTime(), c)
		}
	}

	return annots, errs
}

func isSameMetric(ts *mimirpb.TimeSeries, lbls []mimirpb.LabelAdapter) bool {
	if len(ts.Labels) != len(lbls) {
		return false
	}
	for i, l := range ts.Labels {
		if l.Name != ts.Labels[i].Name || l.Value != ts.Labels[i].Value {
			return false
		}
	}
	return true
}

// addExemplars adds exemplars for the dataPoint. For each exemplar, if it can find a bucket bound corresponding to its value,
// the exemplar is added to the bucket bound's time series, provided that the time series' has samples.
func (c *MimirConverter) addExemplars(ctx context.Context, dataPoint pmetric.HistogramDataPoint, bucketBounds []bucketBoundsData) error {
	if len(bucketBounds) == 0 {
		return nil
	}

	exemplars, err := getPromExemplars(ctx, &c.everyN, dataPoint)
	if err != nil {
		return err
	}
	if len(exemplars) == 0 {
		return nil
	}

	sort.Sort(byBucketBoundsData(bucketBounds))
	for _, exemplar := range exemplars {
		for _, bound := range bucketBounds {
			if err := c.everyN.checkContext(ctx); err != nil {
				return err
			}
			if len(bound.ts.Samples) > 0 && exemplar.Value <= bound.bound {
				bound.ts.Exemplars = append(bound.ts.Exemplars, exemplar)
				break
			}
		}
	}

	return nil
}

// addSample finds a TimeSeries that corresponds to lbls, and adds sample to it.
// If there is no corresponding TimeSeries already, it's created.
// The corresponding TimeSeries is returned.
// If either lbls is nil/empty or sample is nil, nothing is done.
func (c *MimirConverter) addSample(sample *mimirpb.Sample, lbls []mimirpb.LabelAdapter) *mimirpb.TimeSeries {
	if sample == nil || len(lbls) == 0 {
		// This shouldn't happen
		return nil
	}

	ts, _ := c.getOrCreateTimeSeries(lbls)
	ts.Samples = append(ts.Samples, *sample)
	return ts
}

func NewPromoteResourceAttributes(otlpCfg config.OTLPConfig) *PromoteResourceAttributes {
	attrs := otlpCfg.PromoteResourceAttributes
	if otlpCfg.PromoteAllResourceAttributes {
		attrs = otlpCfg.IgnoreResourceAttributes
	}
	attrsMap := make(map[string]struct{}, len(attrs))
	for _, s := range attrs {
		attrsMap[s] = struct{}{}
	}
	return &PromoteResourceAttributes{
		promoteAll: otlpCfg.PromoteAllResourceAttributes,
		attrs:      attrsMap,
	}
}

// promotedAttributes returns labels for promoted resourceAttributes.
func (s *PromoteResourceAttributes) promotedAttributes(resourceAttributes pcommon.Map) []mimirpb.LabelAdapter {
	if s == nil {
		return nil
	}

	var promotedAttrs []mimirpb.LabelAdapter
	if s.promoteAll {
		promotedAttrs = make([]mimirpb.LabelAdapter, 0, resourceAttributes.Len())
		resourceAttributes.Range(func(name string, value pcommon.Value) bool {
			if _, exists := s.attrs[name]; !exists {
				promotedAttrs = append(promotedAttrs, mimirpb.LabelAdapter{Name: name, Value: value.AsString()})
			}
			return true
		})
	} else {
		promotedAttrs = make([]mimirpb.LabelAdapter, 0, len(s.attrs))
		resourceAttributes.Range(func(name string, value pcommon.Value) bool {
			if _, exists := s.attrs[name]; exists {
				promotedAttrs = append(promotedAttrs, mimirpb.LabelAdapter{Name: name, Value: value.AsString()})
			}
			return true
		})
	}
	sort.Stable(ByLabelName(promotedAttrs))
	return promotedAttrs
}

type labelsStringer []mimirpb.LabelAdapter

func (ls labelsStringer) String() string {
	var seriesBuilder strings.Builder
	seriesBuilder.WriteString("{")
	for i, l := range ls {
		if i > 0 {
			seriesBuilder.WriteString(",")
		}
		seriesBuilder.WriteString(fmt.Sprintf("%s=%s", l.Name, l.Value))
	}
	seriesBuilder.WriteString("}")
	return seriesBuilder.String()
}
