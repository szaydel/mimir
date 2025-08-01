// SPDX-License-Identifier: AGPL-3.0-only

package distributor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gogo/protobuf/proto"
	"github.com/gogo/status"
	"github.com/grafana/dskit/grpcutil"
	"github.com/grafana/dskit/httpgrpc"
	"github.com/grafana/dskit/httpgrpc/server"
	"github.com/grafana/dskit/middleware"
	"github.com/grafana/dskit/runutil"
	"github.com/grafana/dskit/tenant"
	"github.com/pierrec/lz4/v4"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/otlptranslator"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/prompb"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"go.uber.org/multierr"
	"google.golang.org/grpc/codes"

	"github.com/grafana/mimir/pkg/distributor/otlp"
	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/util"
	utillog "github.com/grafana/mimir/pkg/util/log"
	"github.com/grafana/mimir/pkg/util/spanlogger"
	"github.com/grafana/mimir/pkg/util/validation"
)

const (
	pbContentType   = "application/x-protobuf"
	jsonContentType = "application/json"

	otelParseError = "otlp_parse_error"
	maxErrMsgLen   = 1024
)

type OTLPHandlerLimits interface {
	OTelMetricSuffixesEnabled(id string) bool
	OTelCreatedTimestampZeroIngestionEnabled(id string) bool
	PromoteOTelResourceAttributes(id string) []string
	OTelKeepIdentifyingResourceAttributes(id string) bool
	OTelConvertHistogramsToNHCB(id string) bool
	OTelPromoteScopeMetadata(id string) bool
	OTelNativeDeltaIngestion(id string) bool
}

// OTLPHandler is an http.Handler accepting OTLP write requests.
func OTLPHandler(
	maxRecvMsgSize int,
	requestBufferPool util.Pool,
	sourceIPs *middleware.SourceIPExtractor,
	limits OTLPHandlerLimits,
	resourceAttributePromotionConfig OTelResourceAttributePromotionConfig,
	retryCfg RetryConfig,
	enableStartTimeQuietZero bool,
	push PushFunc,
	pushMetrics *PushMetrics,
	reg prometheus.Registerer,
	logger log.Logger,
) http.Handler {
	discardedDueToOtelParseError := validation.DiscardedSamplesCounter(reg, otelParseError)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := utillog.WithContext(ctx, logger)
		if sourceIPs != nil {
			source := sourceIPs.Get(r)
			if source != "" {
				logger = utillog.WithSourceIPs(source, logger)
			}
		}

		otlpConverter := newOTLPMimirConverter()

		parser := newOTLPParser(limits, resourceAttributePromotionConfig, otlpConverter, enableStartTimeQuietZero, pushMetrics, discardedDueToOtelParseError)

		supplier := func() (*mimirpb.WriteRequest, func(), error) {
			rb := util.NewRequestBuffers(requestBufferPool)
			var req mimirpb.PreallocWriteRequest
			if err := parser(ctx, r, maxRecvMsgSize, rb, &req, logger); err != nil {
				// Check for httpgrpc error, default to client error if parsing failed
				if _, ok := httpgrpc.HTTPResponseFromError(err); !ok {
					err = httpgrpc.Error(http.StatusBadRequest, err.Error())
				}

				rb.CleanUp()
				return nil, nil, err
			}

			cleanup := func() {
				mimirpb.ReuseSlice(req.Timeseries)
				rb.CleanUp()
			}
			return &req.WriteRequest, cleanup, nil
		}
		req := newRequest(supplier)
		req.contentLength = r.ContentLength

		pushErr := push(ctx, req)
		if pushErr == nil {
			if otlpErr := otlpConverter.Err(); otlpErr != nil {
				// Push was successful, but OTLP converter left out some samples. We let the client know about it by replying with 4xx (and an insight log).
				pushErr = httpgrpc.Error(http.StatusBadRequest, otlpErr.Error())
			} else {
				// Respond as per spec:
				// https://opentelemetry.io/docs/specs/otlp/#otlphttp-response.
				var expResp colmetricpb.ExportMetricsServiceResponse
				addSuccessHeaders(w, req.artificialDelay)
				writeOTLPResponse(r, w, http.StatusOK, &expResp, logger)
				return
			}
		}

		if errors.Is(pushErr, context.Canceled) {
			level.Warn(logger).Log("msg", "push request canceled", "err", pushErr)
			writeErrorToHTTPResponseBody(r, w, statusClientClosedRequest, codes.Canceled, "push request context canceled", logger)
			return
		}
		var (
			httpCode int
			grpcCode codes.Code
			errorMsg string
		)
		if st, ok := grpcutil.ErrorToStatus(pushErr); ok {
			grpcCode = st.Code()
			errorMsg = st.Message()

			// This code is needed for a correct handling of errors returned by the supplier function.
			// These errors are usually created by using the httpgrpc package.
			// However, distributor's write path is complex and has a lot of dependencies, so sometimes it's not.
			if util.IsHTTPStatusCode(grpcCode) {
				httpCode = httpRetryableToOTLPRetryable(int(grpcCode))
			} else {
				httpCode = http.StatusServiceUnavailable
			}
		} else {
			grpcCode, httpCode = toOtlpGRPCHTTPStatus(pushErr)
			errorMsg = pushErr.Error()
		}
		if httpCode != 202 {
			// This error message is consistent with error message in Prometheus remote-write handler, and ingester's ingest-storage pushToStorage method.
			msgs := []interface{}{"msg", "detected an error while ingesting OTLP metrics request (the request may have been partially ingested)", "httpCode", httpCode, "err", pushErr}
			logLevel := level.Error
			if httpCode/100 == 4 {
				msgs = append(msgs, "insight", true)
				logLevel = level.Warn
			}
			logLevel(logger).Log(msgs...)
		}
		addErrorHeaders(w, pushErr, r, httpCode, retryCfg)
		writeErrorToHTTPResponseBody(r, w, httpCode, grpcCode, errorMsg, logger)
	})
}

func newOTLPParser(
	limits OTLPHandlerLimits,
	resourceAttributePromotionConfig OTelResourceAttributePromotionConfig,
	otlpConverter *otlpMimirConverter,
	enableStartTimeQuietZero bool,
	pushMetrics *PushMetrics,
	discardedDueToOtelParseError *prometheus.CounterVec,
) parserFunc {
	return func(ctx context.Context, r *http.Request, maxRecvMsgSize int, buffers *util.RequestBuffers, req *mimirpb.PreallocWriteRequest, logger log.Logger) error {
		contentType := r.Header.Get("Content-Type")
		contentEncoding := r.Header.Get("Content-Encoding")
		var compression util.CompressionType
		switch contentEncoding {
		case "gzip":
			compression = util.Gzip
		case "lz4":
			compression = util.Lz4
		case "":
			compression = util.NoCompression
		default:
			return httpgrpc.Errorf(http.StatusUnsupportedMediaType, "unsupported compression: %s. Only \"gzip\", \"lz4\", or no compression supported", contentEncoding)
		}

		var decoderFunc func(io.Reader) (req pmetricotlp.ExportRequest, uncompressedBodySize int, err error)
		switch contentType {
		case pbContentType:
			decoderFunc = func(reader io.Reader) (req pmetricotlp.ExportRequest, uncompressedBodySize int, err error) {
				exportReq := pmetricotlp.NewExportRequest()
				unmarshaler := otlpProtoUnmarshaler{
					request: &exportReq,
				}
				protoBodySize, err := util.ParseProtoReader(ctx, reader, int(r.ContentLength), maxRecvMsgSize, buffers, unmarshaler, compression)
				var tooLargeErr util.MsgSizeTooLargeErr
				if errors.As(err, &tooLargeErr) {
					return exportReq, 0, httpgrpc.Error(http.StatusRequestEntityTooLarge, distributorMaxOTLPRequestSizeErr{
						actual: tooLargeErr.Actual,
						limit:  tooLargeErr.Limit,
					}.Error())
				}
				return exportReq, protoBodySize, err
			}

		case jsonContentType:
			decoderFunc = func(reader io.Reader) (req pmetricotlp.ExportRequest, uncompressedBodySize int, err error) {
				exportReq := pmetricotlp.NewExportRequest()
				sz := int(r.ContentLength)
				if sz > 0 {
					// Extra space guarantees no reallocation
					sz += bytes.MinRead
				}
				buf := buffers.Get(sz)
				switch compression {
				case util.Gzip:
					gzReader, err := gzip.NewReader(reader)
					if err != nil {
						return exportReq, 0, errors.Wrap(err, "create gzip reader")
					}
					defer runutil.CloseWithLogOnErr(logger, gzReader, "close gzip reader")
					reader = gzReader
				case util.Lz4:
					reader = io.NopCloser(lz4.NewReader(reader))
				}

				reader = http.MaxBytesReader(nil, io.NopCloser(reader), int64(maxRecvMsgSize))
				if _, err := buf.ReadFrom(reader); err != nil {
					if util.IsRequestBodyTooLarge(err) {
						return exportReq, 0, httpgrpc.Error(http.StatusRequestEntityTooLarge, distributorMaxOTLPRequestSizeErr{
							actual: -1,
							limit:  maxRecvMsgSize,
						}.Error())
					}

					return exportReq, 0, errors.Wrap(err, "read write request")
				}

				return exportReq, buf.Len(), exportReq.UnmarshalJSON(buf.Bytes())
			}

		default:
			return httpgrpc.Errorf(http.StatusUnsupportedMediaType, "unsupported content type: %s, supported: [%s, %s]", contentType, jsonContentType, pbContentType)
		}

		// Check the request size against the message size limit, regardless of whether the request is compressed.
		// If the request is compressed and its compressed length already exceeds the size limit, there's no need to decompress it.
		if r.ContentLength > int64(maxRecvMsgSize) {
			return httpgrpc.Error(http.StatusRequestEntityTooLarge, distributorMaxOTLPRequestSizeErr{
				actual: int(r.ContentLength),
				limit:  maxRecvMsgSize,
			}.Error())
		}

		spanLogger, ctx := spanlogger.New(ctx, logger, tracer, "Distributor.OTLPHandler.decodeAndConvert")
		defer spanLogger.Finish()

		spanLogger.SetTag("content_type", contentType)
		spanLogger.SetTag("content_encoding", contentEncoding)
		spanLogger.SetTag("content_length", r.ContentLength)

		otlpReq, uncompressedBodySize, err := decoderFunc(r.Body)
		if err != nil {
			return err
		}

		level.Debug(spanLogger).Log("msg", "decoding complete, starting conversion")

		tenantID, err := tenant.TenantID(ctx)
		if err != nil {
			return err
		}
		addSuffixes := limits.OTelMetricSuffixesEnabled(tenantID)
		enableCTZeroIngestion := limits.OTelCreatedTimestampZeroIngestionEnabled(tenantID)
		if resourceAttributePromotionConfig == nil {
			resourceAttributePromotionConfig = limits
		}
		promoteResourceAttributes := resourceAttributePromotionConfig.PromoteOTelResourceAttributes(tenantID)
		keepIdentifyingResourceAttributes := limits.OTelKeepIdentifyingResourceAttributes(tenantID)
		convertHistogramsToNHCB := limits.OTelConvertHistogramsToNHCB(tenantID)
		promoteScopeMetadata := limits.OTelPromoteScopeMetadata(tenantID)
		allowDeltaTemporality := limits.OTelNativeDeltaIngestion(tenantID)

		pushMetrics.IncOTLPRequest(tenantID)
		pushMetrics.ObserveUncompressedBodySize(tenantID, float64(uncompressedBodySize))

		metrics, metricsDropped, err := otelMetricsToTimeseries(
			ctx,
			otlpConverter,
			otlpReq.Metrics(),
			conversionOptions{
				addSuffixes:                       addSuffixes,
				enableCTZeroIngestion:             enableCTZeroIngestion,
				enableStartTimeQuietZero:          enableStartTimeQuietZero,
				keepIdentifyingResourceAttributes: keepIdentifyingResourceAttributes,
				convertHistogramsToNHCB:           convertHistogramsToNHCB,
				promoteScopeMetadata:              promoteScopeMetadata,
				promoteResourceAttributes:         promoteResourceAttributes,
				allowDeltaTemporality:             allowDeltaTemporality,
			},
			spanLogger,
		)
		if metricsDropped > 0 {
			discardedDueToOtelParseError.WithLabelValues(tenantID, "").Add(float64(metricsDropped)) // "group" label is empty here as metrics couldn't be parsed
		}
		if err != nil {
			return err
		}

		metricCount := len(metrics)
		sampleCount := 0
		histogramCount := 0
		exemplarCount := 0

		for _, m := range metrics {
			sampleCount += len(m.Samples)
			histogramCount += len(m.Histograms)
			exemplarCount += len(m.Exemplars)
		}

		level.Debug(spanLogger).Log(
			"msg", "OTLP to Prometheus conversion complete",
			"metric_count", metricCount,
			"metrics_dropped", metricsDropped,
			"sample_count", sampleCount,
			"histogram_count", histogramCount,
			"exemplar_count", exemplarCount,
			"promoted_resource_attributes", promoteResourceAttributes,
		)

		req.Timeseries = metrics
		req.Metadata = otelMetricsToMetadata(addSuffixes, otlpReq.Metrics())

		return nil
	}
}

// toOtlpGRPCHTTPStatus is utilized by the OTLP endpoint.
func toOtlpGRPCHTTPStatus(pushErr error) (codes.Code, int) {
	var distributorErr Error
	if errors.Is(pushErr, context.DeadlineExceeded) || !errors.As(pushErr, &distributorErr) {
		return codes.Internal, http.StatusServiceUnavailable
	}

	grpcStatusCode := errorCauseToGRPCStatusCode(distributorErr.Cause(), false)
	httpStatusCode := errorCauseToHTTPStatusCode(distributorErr.Cause(), false)
	otlpHTTPStatusCode := httpRetryableToOTLPRetryable(httpStatusCode)
	return grpcStatusCode, otlpHTTPStatusCode
}

// httpRetryableToOTLPRetryable maps non-retryable 5xx HTTP status codes according
// to the OTLP specifications (https://opentelemetry.io/docs/specs/otlp/#failures-1)
// to http.StatusServiceUnavailable. In case of a non-retryable HTTP status code,
// httpRetryableToOTLPRetryable returns the HTTP status code itself.
// Unlike Prometheus, which retries 429 and all 5xx HTTP status codes,
// the OTLP client only retries on HTTP status codes 429, 502, 503, and 504.
func httpRetryableToOTLPRetryable(httpStatusCode int) int {
	if httpStatusCode/100 == 5 {
		mask := httpStatusCode % 100
		// We map all 5xx except 502, 503 and 504 into 503.
		if mask <= 1 || mask > 4 {
			return http.StatusServiceUnavailable
		}
	}
	return httpStatusCode
}

// writeErrorToHTTPResponseBody converts the given error into a gRPC status and marshals it into a byte slice, in order to be written to the response body.
// See doc https://opentelemetry.io/docs/specs/otlp/#failures-1.
func writeErrorToHTTPResponseBody(r *http.Request, w http.ResponseWriter, httpCode int, grpcCode codes.Code, msg string, logger log.Logger) {
	validUTF8Msg := validUTF8Message(msg)
	if server.IsHandledByHttpgrpcServer(r.Context()) {
		w.Header().Set(server.ErrorMessageHeaderKey, validUTF8Msg) // If httpgrpc Server wants to convert this HTTP response into error, use this error message, instead of using response body.
	}

	st := status.New(grpcCode, validUTF8Msg).Proto()
	writeOTLPResponse(r, w, httpCode, st, logger)
}

func writeOTLPResponse(r *http.Request, w http.ResponseWriter, httpCode int, payload proto.Message, logger log.Logger) {
	contentType := r.Header.Get("Content-Type")
	switch contentType {
	case jsonContentType, pbContentType:
	default:
		// Default to protobuf encoding.
		contentType = pbContentType
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")

	body, _, err := marshal(payload, contentType)
	if err != nil {
		httpCode = http.StatusInternalServerError
		level.Error(logger).Log("msg", "failed to marshal payload", "err", err, "payload", payload, "content_type", contentType)
		var format string
		body, format, err = marshal(status.New(codes.Internal, "failed to marshal OTLP response").Proto(), contentType)
		err = errors.Wrapf(err, "marshalling %T to %s", payload, format)
	}
	if err != nil {
		level.Error(logger).Log("msg", "OTLP response marshal failed, responding without payload", "err", err, "content_type", contentType)
		contentType = "text/plain"
		body = nil
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(httpCode)
	if len(body) == 0 {
		return
	}
	if _, err := w.Write(body); err != nil {
		level.Error(logger).Log("msg", "failed to write OTLP error response", "err", err, "content_type", contentType)
	}
}

func marshal(payload proto.Message, contentType string) ([]byte, string, error) {
	if contentType == jsonContentType {
		data, err := json.Marshal(payload)
		return data, "JSON", err
	}

	data, err := proto.Marshal(payload)
	return data, "protobuf", err
}

// otlpProtoUnmarshaler implements proto.Message wrapping pmetricotlp.ExportRequest.
type otlpProtoUnmarshaler struct {
	request *pmetricotlp.ExportRequest
}

func (o otlpProtoUnmarshaler) ProtoMessage() {}

func (o otlpProtoUnmarshaler) Reset() {}

func (o otlpProtoUnmarshaler) String() string {
	return ""
}

func (o otlpProtoUnmarshaler) Unmarshal(data []byte) error {
	return o.request.UnmarshalProto(data)
}

func otelMetricTypeToMimirMetricType(otelMetric pmetric.Metric) mimirpb.MetricMetadata_MetricType {
	switch otelMetric.Type() {
	case pmetric.MetricTypeGauge:
		return mimirpb.GAUGE
	case pmetric.MetricTypeSum:
		metricType := mimirpb.GAUGE
		if otelMetric.Sum().IsMonotonic() {
			metricType = mimirpb.COUNTER
		}
		return metricType
	case pmetric.MetricTypeHistogram:
		return mimirpb.HISTOGRAM
	case pmetric.MetricTypeSummary:
		return mimirpb.SUMMARY
	case pmetric.MetricTypeExponentialHistogram:
		return mimirpb.HISTOGRAM
	}
	return mimirpb.UNKNOWN
}

func otelMetricsToMetadata(addSuffixes bool, md pmetric.Metrics) []*mimirpb.MetricMetadata {
	resourceMetricsSlice := md.ResourceMetrics()

	metadataLength := 0
	for i := 0; i < resourceMetricsSlice.Len(); i++ {
		scopeMetricsSlice := resourceMetricsSlice.At(i).ScopeMetrics()
		for j := 0; j < scopeMetricsSlice.Len(); j++ {
			metadataLength += scopeMetricsSlice.At(j).Metrics().Len()
		}
	}

	namer := otlptranslator.MetricNamer{
		Namespace:          "",
		WithMetricSuffixes: addSuffixes,
		UTF8Allowed:        false,
	}

	metadata := make([]*mimirpb.MetricMetadata, 0, metadataLength)
	for i := 0; i < resourceMetricsSlice.Len(); i++ {
		scopeMetricsSlice := resourceMetricsSlice.At(i).ScopeMetrics()
		for j := 0; j < scopeMetricsSlice.Len(); j++ {
			scopeMetrics := scopeMetricsSlice.At(j)
			for k := 0; k < scopeMetrics.Metrics().Len(); k++ {
				metric := scopeMetrics.Metrics().At(k)
				entry := mimirpb.MetricMetadata{
					Type: otelMetricTypeToMimirMetricType(metric),
					// TODO(krajorama): when UTF-8 is configurable from user limits, use BuildMetricName. See https://github.com/prometheus/prometheus/pull/15664
					MetricFamilyName: namer.Build(otlp.TranslatorMetricFromOtelMetric(metric)),
					Help:             metric.Description(),
					Unit:             metric.Unit(),
				}
				metadata = append(metadata, &entry)
			}
		}
	}

	return metadata
}

type conversionOptions struct {
	addSuffixes                       bool
	enableCTZeroIngestion             bool
	enableStartTimeQuietZero          bool
	keepIdentifyingResourceAttributes bool
	convertHistogramsToNHCB           bool
	promoteScopeMetadata              bool
	promoteResourceAttributes         []string
	allowDeltaTemporality             bool
}

func otelMetricsToTimeseries(
	ctx context.Context,
	converter *otlpMimirConverter,
	md pmetric.Metrics,
	opts conversionOptions,
	logger log.Logger,
) ([]mimirpb.PreallocTimeseries, int, error) {
	settings := otlp.Settings{
		AddMetricSuffixes:                   opts.addSuffixes,
		EnableCreatedTimestampZeroIngestion: opts.enableCTZeroIngestion,
		EnableStartTimeQuietZero:            opts.enableStartTimeQuietZero,
		PromoteResourceAttributes:           otlp.NewPromoteResourceAttributes(config.OTLPConfig{PromoteResourceAttributes: opts.promoteResourceAttributes}),
		KeepIdentifyingResourceAttributes:   opts.keepIdentifyingResourceAttributes,
		ConvertHistogramsToNHCB:             opts.convertHistogramsToNHCB,
		PromoteScopeMetadata:                opts.promoteScopeMetadata,
		AllowDeltaTemporality:               opts.allowDeltaTemporality,
	}
	mimirTS := converter.ToTimeseries(ctx, md, settings, logger)

	dropped := converter.DroppedTotal()
	if len(mimirTS) == 0 && dropped > 0 {
		return nil, dropped, converter.Err()
	}
	return mimirTS, dropped, nil
}

type otlpMimirConverter struct {
	converter *otlp.MimirConverter
	// err holds OTLP parse errors
	err error
}

func newOTLPMimirConverter() *otlpMimirConverter {
	return &otlpMimirConverter{
		converter: otlp.NewMimirConverter(),
	}
}

func (c *otlpMimirConverter) ToTimeseries(ctx context.Context, md pmetric.Metrics, settings otlp.Settings, logger log.Logger) []mimirpb.PreallocTimeseries {
	if c.err != nil {
		return nil
	}

	_, c.err = c.converter.FromMetrics(ctx, md, settings, utillog.SlogFromGoKit(logger))
	return c.converter.TimeSeries()
}

func (c *otlpMimirConverter) DroppedTotal() int {
	if c.err != nil {
		return len(multierr.Errors(c.err))
	}
	return 0
}

func (c *otlpMimirConverter) Err() error {
	if c.err != nil {
		errMsg := c.err.Error()
		if len(errMsg) > maxErrMsgLen {
			errMsg = errMsg[:maxErrMsgLen]
		}
		return fmt.Errorf("otlp parse error: %s", errMsg)
	}
	return nil
}

// TimeseriesToOTLPRequest is used in tests.
// If you provide exemplars they will be placed on the first float or
// histogram sample.
func TimeseriesToOTLPRequest(timeseries []prompb.TimeSeries, metadata []mimirpb.MetricMetadata) pmetricotlp.ExportRequest {
	d := pmetric.NewMetrics()

	for i, ts := range timeseries {
		name := ""
		attributes := pcommon.NewMap()

		for _, l := range ts.Labels {
			if l.Name == model.MetricNameLabel {
				name = l.Value
				continue
			}

			attributes.PutStr(l.Name, l.Value)
		}

		rms := d.ResourceMetrics()
		rm := rms.AppendEmpty()
		rm.Resource().Attributes().PutStr("resource.attr", "value")
		sm := rm.ScopeMetrics()

		if len(ts.Samples) > 0 {
			metric := sm.AppendEmpty().Metrics().AppendEmpty()
			metric.SetName(name)
			metric.SetEmptyGauge()
			if metadata != nil {
				metric.SetDescription(metadata[i].GetHelp())
				metric.SetUnit(metadata[i].GetUnit())
			}
			for i, sample := range ts.Samples {
				datapoint := metric.Gauge().DataPoints().AppendEmpty()
				datapoint.SetTimestamp(pcommon.Timestamp(sample.Timestamp * time.Millisecond.Nanoseconds()))
				datapoint.SetDoubleValue(sample.Value)
				attributes.CopyTo(datapoint.Attributes())
				if i == 0 {
					for _, tsEx := range ts.Exemplars {
						ex := datapoint.Exemplars().AppendEmpty()
						ex.SetDoubleValue(tsEx.Value)
						ex.SetTimestamp(pcommon.Timestamp(tsEx.Timestamp * time.Millisecond.Nanoseconds()))
						ex.FilteredAttributes().EnsureCapacity(len(tsEx.Labels))
						for _, label := range tsEx.Labels {
							ex.FilteredAttributes().PutStr(label.Name, label.Value)
						}
					}
				}
			}
		}

		if len(ts.Histograms) > 0 {
			metric := sm.AppendEmpty().Metrics().AppendEmpty()
			metric.SetName(name)
			metric.SetEmptyExponentialHistogram()
			if metadata != nil {
				metric.SetDescription(metadata[i].GetHelp())
				metric.SetUnit(metadata[i].GetUnit())
			}
			metric.ExponentialHistogram().SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			for i, histogram := range ts.Histograms {
				datapoint := metric.ExponentialHistogram().DataPoints().AppendEmpty()
				datapoint.SetTimestamp(pcommon.Timestamp(histogram.Timestamp * time.Millisecond.Nanoseconds()))
				datapoint.SetScale(histogram.Schema)
				datapoint.SetCount(histogram.GetCountInt())

				offset, counts := translateBucketsLayout(histogram.PositiveSpans, histogram.PositiveDeltas)
				datapoint.Positive().SetOffset(offset)
				datapoint.Positive().BucketCounts().FromRaw(counts)

				offset, counts = translateBucketsLayout(histogram.NegativeSpans, histogram.NegativeDeltas)
				datapoint.Negative().SetOffset(offset)
				datapoint.Negative().BucketCounts().FromRaw(counts)

				datapoint.SetSum(histogram.GetSum())
				datapoint.SetZeroCount(histogram.GetZeroCountInt())
				attributes.CopyTo(datapoint.Attributes())
				if i == 0 {
					for _, tsEx := range ts.Exemplars {
						ex := datapoint.Exemplars().AppendEmpty()
						ex.SetDoubleValue(histogram.Sum / 10.0) // Doesn't really matter, just a placeholder
						ex.SetTimestamp(pcommon.Timestamp(histogram.Timestamp * time.Millisecond.Nanoseconds()))
						ex.FilteredAttributes().EnsureCapacity(len(tsEx.Labels))
						for _, label := range tsEx.Labels {
							ex.FilteredAttributes().PutStr(label.Name, label.Value)
						}
					}
				}
			}
		}
	}

	return pmetricotlp.NewExportRequestFromMetrics(d)
}

// translateBucketLayout the test function that translates the Prometheus native histograms buckets
// layout to the OTel exponential histograms sparse buckets layout. It is the inverse function to
// https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/47471382940a0d794a387b06c99413520f0a68f8/pkg/translator/prometheusremotewrite/histograms.go#L118
func translateBucketsLayout(spans []prompb.BucketSpan, deltas []int64) (int32, []uint64) {
	if len(spans) == 0 {
		return 0, []uint64{}
	}

	firstSpan := spans[0]
	bucketsCount := int(firstSpan.Length)
	for i := 1; i < len(spans); i++ {
		bucketsCount += int(spans[i].Offset) + int(spans[i].Length)
	}
	buckets := make([]uint64, bucketsCount)

	bucketIdx := 0
	deltaIdx := 0
	currCount := int64(0)

	// set offset of the first span to 0 to simplify translation
	spans[0].Offset = 0
	for _, span := range spans {
		bucketIdx += int(span.Offset)
		for i := 0; i < int(span.GetLength()); i++ {
			currCount += deltas[deltaIdx]
			buckets[bucketIdx] = uint64(currCount)
			deltaIdx++
			bucketIdx++
		}
	}

	return firstSpan.Offset - 1, buckets
}
