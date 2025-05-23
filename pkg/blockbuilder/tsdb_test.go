// SPDX-License-Identifier: AGPL-3.0-only

package blockbuilder

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/flagext"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/storage/ingest"
	mimir_tsdb "github.com/grafana/mimir/pkg/storage/tsdb"
	"github.com/grafana/mimir/pkg/util/test"
	"github.com/grafana/mimir/pkg/util/validation"
)

func createWriteRequest(t *testing.T, suffix string, samples []mimirpb.Sample, histograms []mimirpb.Histogram) []byte {
	req := mimirpb.WriteRequest{}

	var seriesValue string
	if len(histograms) > 0 {
		seriesValue = "histogram"
	} else {
		seriesValue = "float"
	}
	req.Timeseries = append(req.Timeseries, mimirpb.PreallocTimeseries{
		TimeSeries: &mimirpb.TimeSeries{
			Labels: []mimirpb.LabelAdapter{
				{Name: "foo", Value: fmt.Sprintf("%s%s", seriesValue, suffix)},
			},
			Samples:    samples,
			Histograms: histograms,
		},
	})

	data, err := req.Marshal()
	require.NoError(t, err)

	return data
}

func floatSample(ts int64, val float64) []mimirpb.Sample {
	return []mimirpb.Sample{{TimestampMs: ts, Value: val}}
}

func histogramSample(ts int64) []mimirpb.Histogram {
	return []mimirpb.Histogram{
		mimirpb.FromHistogramToHistogramProto(ts, test.GenerateTestHistogram(int(ts))),
	}
}

func TestTSDBBuilder(t *testing.T) {
	userID := strconv.Itoa(rand.Int())

	// Set OOO window and other overrides for testing tenant.
	limits := map[string]*validation.Limits{
		userID: {
			OutOfOrderTimeWindow:             model.Duration(30 * time.Minute),
			NativeHistogramsIngestionEnabled: true,
		},
	}
	overrides := validation.NewOverrides(defaultLimitsTestConfig(), validation.NewMockTenantLimits(limits))

	// Hold samples for all cases and check for the correctness.
	var expSamples []mimirpb.Sample
	var expHistograms []mimirpb.Histogram

	createRequest := func(userID string, samples []mimirpb.Sample, histograms []mimirpb.Histogram, mustAccept bool) *kgo.Record {
		if mustAccept {
			expSamples = append(expSamples, samples...)
			for i := range histograms {
				histograms[i].ResetHint = 0
				expHistograms = append(expHistograms, histograms[i])
			}
		}
		return &kgo.Record{
			Key:   []byte(userID),
			Value: createWriteRequest(t, "", samples, histograms),
		}
	}
	addFloatSample := func(builder *TSDBBuilder, ts int64, val float64, lastEnd, currEnd int64, recordProcessedBefore, wantAccepted bool) {
		rec := createRequest(userID, floatSample(ts, val), nil, wantAccepted)
		allProcessed, err := builder.Process(context.Background(), rec, lastEnd, currEnd, recordProcessedBefore, false)
		require.NoError(t, err)
		require.Equal(t, wantAccepted, allProcessed)
	}
	addHistogramSample := func(builder *TSDBBuilder, ts int64, lastEnd, currEnd int64, recordProcessedBefore, wantAccepted bool) {
		rec := createRequest(userID, nil, histogramSample(ts), wantAccepted)
		allProcessed, err := builder.Process(context.Background(), rec, lastEnd, currEnd, recordProcessedBefore, false)
		require.NoError(t, err)
		require.Equal(t, wantAccepted, allProcessed)
	}

	processingRange := time.Hour.Milliseconds()
	blockRange := 2 * time.Hour.Milliseconds()

	testCases := []struct {
		name                        string
		lastEnd, currEnd            int64
		verifyBlocksAfterCompaction func(blocks []*tsdb.Block)
	}{
		{
			name:    "current start is at even hour",
			lastEnd: 2 * processingRange,
			currEnd: 3 * processingRange,
			verifyBlocksAfterCompaction: func(blocks []*tsdb.Block) {
				require.Len(t, blocks, 5) // 4 blocks for main userID, and 1 for ooo-user

				lastEnd := 2 * processingRange
				// One in-order and one out-of-order block for the previous range.
				require.Equal(t, lastEnd-blockRange, blocks[0].MinTime())
				require.Equal(t, lastEnd, blocks[0].MaxTime())
				require.Equal(t, lastEnd-blockRange, blocks[1].MinTime())
				require.Equal(t, lastEnd, blocks[1].MaxTime())
				// One in-order and one out-of-order block for the current range.
				require.Equal(t, lastEnd, blocks[3].MinTime())
				require.Equal(t, lastEnd+blockRange, blocks[3].MaxTime())
				require.Equal(t, lastEnd, blocks[2].MinTime())
				require.Equal(t, lastEnd+blockRange, blocks[2].MaxTime())
			},
		},
		{
			name:    "current start is at odd hour",
			lastEnd: 3 * processingRange,
			currEnd: 4 * processingRange,
			verifyBlocksAfterCompaction: func(blocks []*tsdb.Block) {
				require.Len(t, blocks, 3) // 2 blocks for main userID, and 1 for ooo-user

				currEnd := 4 * processingRange
				// Both in-order and out-of-order blocks are in the same block range.
				require.Equal(t, currEnd-blockRange, blocks[0].MinTime())
				require.Equal(t, currEnd, blocks[0].MaxTime())
				require.Equal(t, currEnd-blockRange, blocks[1].MinTime())
				require.Equal(t, currEnd, blocks[1].MaxTime())
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expSamples = expSamples[:0]
			expHistograms = expHistograms[:0]
			metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
			builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)

			currEnd, lastEnd := tc.currEnd, tc.lastEnd
			{ // Add float samples.
				// 1. Processing records that were processed before (they come first in real world).
				// A. This sample is already processed. So it should be ignored but say all processed
				//    because it is already in a block.
				addFloatSample(builder, lastEnd-10, 1, lastEnd, currEnd, true, true)
				// Since this is already processed, it should not be added to the expected samples.
				expSamples = expSamples[:0]
				// B. This goes in this block.
				addFloatSample(builder, lastEnd+100, 1, lastEnd, currEnd, true, true)
				// C. This sample should be processed in the future.
				addFloatSample(builder, currEnd+1, 1, lastEnd, currEnd, true, false)

				// 2. Processing records that were not processed before.
				// A. Sample that belonged to previous processing period but came in late. Processed in current cycle.
				addFloatSample(builder, lastEnd-5, 1, lastEnd, currEnd, false, true)
				// B. Sample that belongs to the current processing period.
				addFloatSample(builder, lastEnd+200, 1, lastEnd, currEnd, false, true)
				// C. Sample that belongs to the current processing period but is a duplicate with different value.
				addFloatSample(builder, lastEnd+200, 2, lastEnd, currEnd, false, true)
				// The request is accepted, but its sample won't end up in the DB due to soft "ErrDuplicateSampleForTimestamp".
				expSamples = expSamples[:len(expSamples)-1]
				// D. This sample should be processed in the future.
				addFloatSample(builder, currEnd+2, 1, lastEnd, currEnd, false, false)
				// E. This sample is too old (soft error).
				addFloatSample(builder, 0, 1, lastEnd, currEnd, false, true)
				expSamples = expSamples[:len(expSamples)-1]

				// 3. Out of order sample in a new record.
				// A. In the current range but out of order w.r.t. the previous sample.
				addFloatSample(builder, lastEnd+20, 1, lastEnd, currEnd, false, true)
				// B. Before current range and out of order w.r.t. the previous sample. Already covered above, but this
				// exists to explicitly state the case.
				addFloatSample(builder, lastEnd-20, 1, lastEnd, currEnd, false, true)
			}
			{ // Add native histogram samples.
				// 1.A from above.
				addHistogramSample(builder, lastEnd-10, lastEnd, currEnd, true, true)
				expHistograms = expHistograms[:0]

				// 2.A from above. Although in real world recordProcessedBefore=false will only come after all recordProcessedBefore=true
				// are done, we are inserting it here because native histograms do not support out-of-order samples yet.
				// This sample here goes in the in-order block unlike above where 2.A goes in out-of-order block.
				addHistogramSample(builder, lastEnd-5, lastEnd, currEnd, false, true)

				// 1.B from above.
				addHistogramSample(builder, lastEnd+100, lastEnd, currEnd, true, true)
				// 1.C from above.
				addHistogramSample(builder, currEnd+1, lastEnd, currEnd, true, false)

				// 2.B from above.
				addHistogramSample(builder, lastEnd+200, lastEnd, currEnd, false, true)
				// 2.C from above.
				addHistogramSample(builder, currEnd+2, lastEnd, currEnd, false, false)

				// 3.A and 3.B not done. TODO: do it when out-of-order histograms are supported.
			}
			{
				// Out of order sample with no OOO window configured for the tenant.
				userID := "test-ooo-tenant"

				// This one goes into the block.
				samples := floatSample(lastEnd+20, 1)
				rec := createRequest(userID, samples, nil, false)
				allProcessed, err := builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)
				require.NoError(t, err)
				require.True(t, allProcessed)
				expOOOSamples := append([]mimirpb.Sample(nil), samples...)

				// This one doesn't go into the block because of "ErrOutOfOrderSample" (soft error)
				samples = floatSample(lastEnd-20, 1)
				rec = createRequest(userID, samples, nil, false)
				allProcessed, err = builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)
				require.NoError(t, err)
				require.True(t, allProcessed)

				tenant := tsdbTenant{
					partitionID: rec.Partition,
					tenantID:    userID,
				}
				db, err := builder.getOrCreateTSDB(tenant)
				require.NoError(t, err)

				// Check expected out of order samples in the DB.
				compareQuery(t, db.DB, expOOOSamples, nil, labels.MustNewMatcher(labels.MatchRegexp, "foo", ".*"))
			}

			// Query the TSDB for the expected samples.
			tenant := tsdbTenant{
				partitionID: 0,
				tenantID:    userID,
			}
			db, err := builder.getOrCreateTSDB(tenant)
			require.NoError(t, err)

			// Check the samples in the DB.
			compareQuery(t, db.DB, expSamples, expHistograms, labels.MustNewMatcher(labels.MatchRegexp, "foo", ".*"))

			// This should create the appropriate blocks and close the DB.
			shipperDir := t.TempDir()
			_, err = builder.CompactAndUpload(context.Background(), mockUploaderFunc(t, shipperDir))
			require.NoError(t, err)
			require.Nil(t, builder.tsdbs[tenant])

			newDB, err := tsdb.Open(shipperDir, promslog.NewNopLogger(), nil, nil, nil)
			require.NoError(t, err)

			// One for the in-order current range. Two for the out-of-order blocks: one for the current range
			// and one for the previous range.
			blocks := newDB.Blocks()
			tc.verifyBlocksAfterCompaction(blocks)

			// Check correctness of samples in the blocks.
			compareQuery(t, newDB, expSamples, expHistograms, labels.MustNewMatcher(labels.MatchRegexp, "foo", ".*"))
			require.NoError(t, newDB.Close())
		})
	}
}

func TestTSDBBuilder_CompactAndUpload_fail(t *testing.T) {
	overrides := validation.NewOverrides(defaultLimitsTestConfig(), nil)
	metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
	builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)
	t.Cleanup(func() {
		require.NoError(t, builder.Close())
	})

	userID := strconv.Itoa(rand.Int())
	tenant := tsdbTenant{
		partitionID: 0,
		tenantID:    userID,
	}
	_, err := builder.getOrCreateTSDB(tenant)
	require.NoError(t, err)

	errUploadFailed := fmt.Errorf("upload failed")
	_, err = builder.CompactAndUpload(context.Background(), func(_ context.Context, _, _ string, _ []tsdb.BlockMeta) error {
		return errUploadFailed
	})
	require.ErrorIs(t, err, errUploadFailed)
}

func compareQueryWithDir(t *testing.T, bucketDir string, expSamples []mimirpb.Sample, expHistograms []mimirpb.Histogram, matchers ...*labels.Matcher) *tsdb.DB {
	db, err := tsdb.Open(bucketDir, promslog.NewNopLogger(), nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	compareQuery(t, db, expSamples, expHistograms, matchers...)

	return db
}

func compareQuery(t *testing.T, db *tsdb.DB, expSamples []mimirpb.Sample, expHistograms []mimirpb.Histogram, matchers ...*labels.Matcher) {
	querier, err := db.Querier(math.MinInt64, math.MaxInt64)
	require.NoError(t, err)
	ss := querier.Select(
		context.Background(), true, nil,
		matchers...,
	)

	var actSamples []mimirpb.Sample
	var actHistograms []mimirpb.Histogram
	for ss.Next() {
		series := ss.At()

		fooVal := series.Labels().Get("foo")
		require.True(t, strings.HasPrefix(fooVal, "float") ||
			strings.HasPrefix(fooVal, "histogram"))

		it := series.Iterator(nil)
		for typ := it.Next(); typ != chunkenc.ValNone; typ = it.Next() {
			switch typ {
			case chunkenc.ValFloat:
				ts, val := it.At()
				actSamples = append(actSamples, mimirpb.Sample{TimestampMs: ts, Value: val})
			case chunkenc.ValHistogram:
				ts, h := it.AtHistogram(nil)
				hp := mimirpb.FromHistogramToHistogramProto(ts, h)
				hp.ResetHint = 0
				actHistograms = append(actHistograms, hp)
			default:
				t.Fatalf("unexpected sample type %v", typ)
			}
		}
		require.NoError(t, it.Err())
	}

	require.NoError(t, ss.Err())
	require.NoError(t, querier.Close())

	sort.Slice(expSamples, func(i, j int) bool {
		return expSamples[i].TimestampMs < expSamples[j].TimestampMs
	})
	sort.Slice(expHistograms, func(i, j int) bool {
		return expHistograms[i].Timestamp < expHistograms[j].Timestamp
	})
	require.Equal(t, expSamples, actSamples)
	require.Equal(t, expHistograms, actHistograms)
}

func mockUploaderFunc(t *testing.T, destDir string) blockUploader {
	return func(_ context.Context, _, dbDir string, metas []tsdb.BlockMeta) error {
		for _, meta := range metas {
			blockDir := path.Join(dbDir, meta.ULID.String())
			err := os.Rename(blockDir, path.Join(destDir, path.Base(blockDir)))
			require.NoError(t, err)
		}
		return nil
	}
}

// It is important that processing empty request is a success, as in says all samples were processed,
// so that checkpointing can be done correctly.
func TestProcessingEmptyRequest(t *testing.T) {
	userID := "1"
	lastEnd := 2 * time.Hour.Milliseconds()
	currEnd := lastEnd + time.Hour.Milliseconds()

	overrides := validation.NewOverrides(defaultLimitsTestConfig(), nil)
	metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
	builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)

	// Has a timeseries with no samples.
	var rec kgo.Record
	rec.Key = []byte(userID)
	req := mimirpb.WriteRequest{}
	req.Timeseries = append(req.Timeseries, mimirpb.PreallocTimeseries{
		TimeSeries: &mimirpb.TimeSeries{
			Labels:  []mimirpb.LabelAdapter{{Name: "foo", Value: "bar"}},
			Samples: []mimirpb.Sample{},
		},
	})
	data, err := req.Marshal()
	require.NoError(t, err)
	rec.Value = data
	allProcessed, err := builder.Process(context.Background(), &rec, lastEnd, currEnd, false, false)
	require.NoError(t, err)
	require.True(t, allProcessed)

	// Has no timeseries.
	req.Timeseries = req.Timeseries[:0]
	data, err = req.Marshal()
	require.NoError(t, err)
	rec.Value = data
	allProcessed, err = builder.Process(context.Background(), &rec, lastEnd, currEnd, false, false)
	require.NoError(t, err)
	require.True(t, allProcessed)

	require.NoError(t, builder.tsdbs[tsdbTenant{0, userID}].Close())
}

func TestTSDBBuilder_KafkaRecordVersion(t *testing.T) {
	t.Run("record version header missing entirely", func(t *testing.T) {
		userID := "1"
		processingRange := time.Hour.Milliseconds()
		lastEnd := 2 * processingRange
		currEnd := 3 * processingRange

		overrides := validation.NewOverrides(defaultLimitsTestConfig(), nil)
		metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
		builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)
		samples := floatSample(lastEnd+20, 1)
		histograms := histogramSample(lastEnd + 40)

		rec := &kgo.Record{
			Key:   []byte(userID),
			Value: createWriteRequest(t, "", samples, histograms),
		}
		success, err := builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)

		require.True(t, success)
		require.NoError(t, err)
	})

	t.Run("record version supported", func(t *testing.T) {
		userID := "1"
		attemptedRecordVersion := 1
		processingRange := time.Hour.Milliseconds()
		lastEnd := 2 * processingRange
		currEnd := 3 * processingRange

		overrides := validation.NewOverrides(defaultLimitsTestConfig(), nil)
		metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
		builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)
		samples := floatSample(lastEnd+20, 1)
		histograms := histogramSample(lastEnd + 40)

		rec := &kgo.Record{
			Key:     []byte(userID),
			Value:   createWriteRequest(t, "", samples, histograms),
			Headers: []kgo.RecordHeader{ingest.RecordVersionHeader(attemptedRecordVersion)},
		}
		success, err := builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)

		require.True(t, success)
		require.NoError(t, err)
	})

	t.Run("record version unsupported", func(t *testing.T) {
		userID := "1"
		attemptedRecordVersion := 101
		processingRange := time.Hour.Milliseconds()
		lastEnd := 2 * processingRange
		currEnd := 3 * processingRange

		overrides := validation.NewOverrides(defaultLimitsTestConfig(), nil)
		metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
		builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)
		samples := floatSample(lastEnd+20, 1)
		histograms := histogramSample(lastEnd + 40)

		rec := &kgo.Record{
			Key:     []byte(userID),
			Value:   createWriteRequest(t, "", samples, histograms),
			Headers: []kgo.RecordHeader{ingest.RecordVersionHeader(attemptedRecordVersion)},
		}
		success, err := builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)

		require.False(t, success)
		require.ErrorContains(t, err, fmt.Sprintf("unsupported version: %d, max supported version: %d", attemptedRecordVersion, ingest.LatestRecordVersion))
	})
}

// TestTSDBBuilderLimits tests the correct enforcements of series limits and also
// that series limit error does not cause the processing to fail (i.e. do not error out).
func TestTSDBBuilderLimits(t *testing.T) {
	var (
		user1 = "user1"
		user2 = "user2"
		// Limits should be applied only if the limits is under 50
		applyGlobalSeriesLimitUnder = 50
	)

	limits := map[string]*validation.Limits{
		user1: {
			MaxGlobalSeriesPerUser:           30,
			NativeHistogramsIngestionEnabled: true,
		},
		user2: {
			MaxGlobalSeriesPerUser:           150,
			NativeHistogramsIngestionEnabled: true,
		},
	}
	overrides := validation.NewOverrides(defaultLimitsTestConfig(), validation.NewMockTenantLimits(limits))

	metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
	builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, applyGlobalSeriesLimitUnder)
	t.Cleanup(func() {
		require.NoError(t, builder.Close())
	})

	var (
		processingRange = time.Hour.Milliseconds()
		lastEnd         = 2 * processingRange
		currEnd         = 3 * processingRange
		ts              = lastEnd + (processingRange / 2)
	)
	createRequest := func(userID string, seriesID int) *kgo.Record {
		var (
			samples    []mimirpb.Sample
			histograms []mimirpb.Histogram
		)
		if seriesID%2 == 0 {
			samples = floatSample(ts, float64(seriesID))
		} else {
			histograms = histogramSample(ts)
		}
		return &kgo.Record{
			Key:   []byte(userID),
			Value: createWriteRequest(t, strconv.Itoa(seriesID), samples, histograms),
		}
	}

	for seriesID := 1; seriesID <= 100; seriesID++ {
		for userID := range limits {
			rec := createRequest(userID, seriesID)
			allProcessed, err := builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)
			require.NoError(t, err)
			require.Equal(t, true, allProcessed)
		}
	}

	// user1 had a limit of 30, which is less than applyGlobalSeriesLimitUnder.
	// So the limit must be applied.
	db, err := builder.getOrCreateTSDB(tsdbTenant{tenantID: user1})
	require.NoError(t, err)
	require.Equal(t, uint64(30), db.Head().NumSeries())

	// user2 had a limit of 100, which is greather than applyGlobalSeriesLimitUnder.
	// So the limit must not be applied.
	db, err = builder.getOrCreateTSDB(tsdbTenant{tenantID: user2})
	require.NoError(t, err)
	require.Equal(t, uint64(100), db.Head().NumSeries())
}

// TestTSDBBuilderNativeHistogramEnabledError tests that when native histograms are disabled for a tenant,
// the TSDB builder does not error out when trying to ingest native histogram for that tenant.
func TestTSDBBuilderNativeHistogramEnabledError(t *testing.T) {
	var (
		user1 = "user1"
		user2 = "user2"
	)

	limits := map[string]*validation.Limits{
		user1: {
			NativeHistogramsIngestionEnabled: true,
		},
		user2: {
			NativeHistogramsIngestionEnabled: false,
		},
	}
	overrides := validation.NewOverrides(defaultLimitsTestConfig(), validation.NewMockTenantLimits(limits))

	metrics := newTSDBBBuilderMetrics(prometheus.NewPedanticRegistry())
	builder := NewTSDBBuilder(log.NewNopLogger(), t.TempDir(), mimir_tsdb.BlocksStorageConfig{}, overrides, metrics, 0)
	t.Cleanup(func() {
		require.NoError(t, builder.Close())
	})

	var (
		processingRange = time.Hour.Milliseconds()
		lastEnd         = 2 * processingRange
		currEnd         = 3 * processingRange
		ts              = lastEnd + (processingRange / 2)
	)
	for seriesID := 1; seriesID <= 100; seriesID++ {
		for userID := range limits {
			rec := &kgo.Record{
				Key:   []byte(userID),
				Value: createWriteRequest(t, strconv.Itoa(seriesID), nil, histogramSample(ts)),
			}
			allProcessed, err := builder.Process(context.Background(), rec, lastEnd, currEnd, false, false)
			require.NoError(t, err)
			require.Equal(t, true, allProcessed)
		}
	}

	// user1 had native histograms enabled. We should see it in the TSDB.
	db, err := builder.getOrCreateTSDB(tsdbTenant{tenantID: user1})
	require.NoError(t, err)
	require.Equal(t, uint64(100), db.Head().NumSeries())

	// user2 had native histograms disabled. Nothing should be in the TSDB.
	db, err = builder.getOrCreateTSDB(tsdbTenant{tenantID: user2})
	require.NoError(t, err)
	require.Equal(t, uint64(0), db.Head().NumSeries())
}

func defaultLimitsTestConfig() validation.Limits {
	limits := validation.Limits{}
	flagext.DefaultValues(&limits)
	return limits
}
