// SPDX-License-Identifier: AGPL-3.0-only

package aggregations

import (
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"

	"github.com/grafana/mimir/pkg/streamingpromql/types"
	"github.com/grafana/mimir/pkg/util/limiter"
)

// AggregationGroup accumulates series that have been grouped together and computes the output series data.
type AggregationGroup interface {
	// AccumulateSeries takes in a series as part of the group
	// remainingSeriesInGroup includes the current series (ie if data is the last series, then remainingSeriesInGroup is 1)
	AccumulateSeries(data types.InstantVectorSeriesData, timeRange types.QueryTimeRange, memoryConsumptionTracker *limiter.MemoryConsumptionTracker, emitAnnotation types.EmitAnnotationFunc, remainingSeriesInGroup uint) error
	// ComputeOutputSeries does any final calculations and returns the grouped series data
	ComputeOutputSeries(param types.ScalarData, timeRange types.QueryTimeRange, memoryConsumptionTracker *limiter.MemoryConsumptionTracker) (types.InstantVectorSeriesData, bool, error)
	// Close releases any resources held by the group.
	// Close is guaranteed to be called at most once per group.
	Close(memoryConsumptionTracker *limiter.MemoryConsumptionTracker)
}

type AggregationGroupFactory func() AggregationGroup

var AggregationGroupFactories = map[parser.ItemType]AggregationGroupFactory{
	parser.AVG:      func() AggregationGroup { return &AvgAggregationGroup{} },
	parser.COUNT:    func() AggregationGroup { return NewCountGroupAggregationGroup(true) },
	parser.GROUP:    func() AggregationGroup { return NewCountGroupAggregationGroup(false) },
	parser.MAX:      func() AggregationGroup { return NewMinMaxAggregationGroup(true) },
	parser.MIN:      func() AggregationGroup { return NewMinMaxAggregationGroup(false) },
	parser.QUANTILE: func() AggregationGroup { return &QuantileAggregationGroup{} },
	parser.STDDEV:   func() AggregationGroup { return NewStddevStdvarAggregationGroup(true) },
	parser.STDVAR:   func() AggregationGroup { return NewStddevStdvarAggregationGroup(false) },
	parser.SUM:      func() AggregationGroup { return &SumAggregationGroup{} },
}

// Sentinel value used to indicate a sample has seen an invalid combination of histograms and should be ignored.
//
// Invalid combinations include exponential and custom buckets, and histograms with incompatible custom buckets.
var invalidCombinationOfHistograms = &histogram.FloatHistogram{}

// SeriesToGroupLabelsBytesFunc is a function that computes a string-like representation of the output group labels for the given input series.
//
// It returns a byte slice rather than a string to make it possible to avoid unnecessarily allocating a string.
//
// The byte slice returned may contain non-printable characters.
//
// Why not just use the labels.Labels computed by the seriesToGroupLabelsFunc and call String() on it?
//
// Most of the time, we don't need the labels.Labels instance, as we expect there are far fewer output groups than input series,
// and we only need the labels.Labels instance once per output group.
// However, we always need to compute the string-like representation for each input series, so we can look up its corresponding
// output group. And we can do this without allocating a string by returning just the bytes that make up the string.
// There's not much point in using the hash of the group labels as we always need the string (or the labels.Labels) to ensure
// there are no hash collisions - so we might as well just go straight to the string-like representation.
//
// Furthermore, labels.Labels.String() doesn't allow us to reuse the buffer used when producing the string or to return a byte slice,
// whereas this method does.
// This saves us allocating a new buffer and string for every single input series, which has a noticeable performance impact.
type SeriesToGroupLabelsBytesFunc func(labels.Labels) []byte

func GroupLabelsBytesFunc(grouping []string, without bool) SeriesToGroupLabelsBytesFunc {
	if len(grouping) == 0 {
		return groupToSingleSeriesLabelsBytesFunc
	}

	// Why 1024 bytes? It's what labels.Labels.String() uses as a buffer size, so we use that as a sensible starting point too.
	b := make([]byte, 0, 1024)
	if without {
		return func(l labels.Labels) []byte {
			// NewAggregation and NewTopKBottomK will add __name__ to Grouping for 'without' aggregations, so no need to add it here.
			b = l.BytesWithoutLabels(b, grouping...)
			return b
		}
	}

	return func(l labels.Labels) []byte {
		b = l.BytesWithLabels(b, grouping...)
		return b
	}
}

var groupToSingleSeriesLabelsBytesFunc = func(_ labels.Labels) []byte { return nil }
