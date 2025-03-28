// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/querier/lazyquery/lazyquery.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package lazyquery

import (
	"context"
	"slices"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
)

// LazyQueryable wraps a storage.Queryable
type LazyQueryable struct {
	q storage.Queryable
}

// Querier implements storage.Queryable
func (lq LazyQueryable) Querier(mint, maxt int64) (storage.Querier, error) {
	q, err := lq.q.Querier(mint, maxt)
	if err != nil {
		return nil, err
	}

	return NewLazyQuerier(q), nil
}

// NewLazyQueryable returns a lazily wrapped queryable
func NewLazyQueryable(q storage.Queryable) storage.Queryable {
	return LazyQueryable{q}
}

// LazyQuerier is a lazy-loaded adapter for a storage.Querier
type LazyQuerier struct {
	next storage.Querier
}

// NewLazyQuerier wraps a storage.Querier, does the Select in the background.
// Return value cannot be used from more than one goroutine simultaneously.
func NewLazyQuerier(next storage.Querier) storage.Querier {
	return LazyQuerier{next}
}

// Select implements Storage.Querier
func (l LazyQuerier) Select(ctx context.Context, selectSorted bool, params *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	// make sure there is space in the buffer, to unblock the goroutine and let it die even if nobody is
	// waiting for the result yet (or anymore).
	future := make(chan storage.SeriesSet, 1)
	copiedParams := copyParams(params)
	go func() {
		future <- l.next.Select(ctx, selectSorted, copiedParams, matchers...)
	}()

	return &lazySeriesSet{
		future: future,
	}
}

func copyParams(params *storage.SelectHints) *storage.SelectHints {
	if params == nil {
		return nil
	}
	copiedParams := *params
	copiedParams.Grouping = slices.Clone(params.Grouping)

	return &copiedParams
}

// LabelValues implements Storage.Querier
func (l LazyQuerier) LabelValues(ctx context.Context, name string, hints *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	return l.next.LabelValues(ctx, name, hints, matchers...)
}

// LabelNames implements Storage.Querier
func (l LazyQuerier) LabelNames(ctx context.Context, hints *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	return l.next.LabelNames(ctx, hints, matchers...)
}

// Close implements Storage.Querier
func (l LazyQuerier) Close() error {
	return l.next.Close()
}

type lazySeriesSet struct {
	next   storage.SeriesSet
	future chan storage.SeriesSet
}

// Next implements storage.SeriesSet.  NB not thread safe!
func (s *lazySeriesSet) Next() bool {
	if s.next == nil {
		s.next = <-s.future
	}
	return s.next.Next()
}

// At implements storage.SeriesSet.
func (s *lazySeriesSet) At() storage.Series {
	if s.next == nil {
		s.next = <-s.future
	}
	return s.next.At()
}

// Err implements storage.SeriesSet.
func (s *lazySeriesSet) Err() error {
	if s.next == nil {
		s.next = <-s.future
	}
	return s.next.Err()
}

// Warnings implements storage.SeriesSet.
func (s *lazySeriesSet) Warnings() annotations.Annotations {
	if s.next == nil {
		s.next = <-s.future
	}
	return s.next.Warnings()
}
