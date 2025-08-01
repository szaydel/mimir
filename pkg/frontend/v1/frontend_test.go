// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/frontend/v1/frontend_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package v1

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/gorilla/mux"
	"github.com/grafana/dskit/flagext"
	httpgrpc_server "github.com/grafana/dskit/httpgrpc/server"
	"github.com/grafana/dskit/middleware"
	"github.com/grafana/dskit/services"
	"github.com/grafana/dskit/tracing"
	"github.com/grafana/dskit/user"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/atomic"
	"google.golang.org/grpc"

	"github.com/grafana/mimir/pkg/frontend/transport"
	"github.com/grafana/mimir/pkg/frontend/v1/frontendv1pb"
	"github.com/grafana/mimir/pkg/querier/stats"
	querier_worker "github.com/grafana/mimir/pkg/querier/worker"
	"github.com/grafana/mimir/pkg/scheduler/queue"
	"github.com/grafana/mimir/pkg/util/httpgrpcutil"
)

func init() {
	// Install OTel tracing, we need it for the tests.
	os.Setenv("OTEL_TRACES_EXPORTER", "none")
	_, err := tracing.NewOTelFromEnv("test", log.NewNopLogger())
	if err != nil {
		panic(err)
	}
}

const (
	query        = "/api/v1/query_range?end=1536716898&query=sum%28container_memory_rss%29+by+%28namespace%29&start=1536673680&step=120"
	responseBody = `{"status":"success","data":{"resultType":"Matrix","result":[{"metric":{"foo":"bar"},"values":[[1536673680,"137"],[1536673780,"137"]]}]}}`
)

func TestFrontend(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("Hello World"))
		require.NoError(t, err)
	})
	test := func(addr string, _ *Frontend) {
		req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/", addr), nil)
		require.NoError(t, err)
		err = user.InjectOrgIDIntoHTTPRequest(user.InjectOrgID(context.Background(), "1"), req)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		assert.Equal(t, "Hello World", string(body))
	}

	testFrontend(t, defaultFrontendConfig(), handler, test, nil, nil)
}

func TestFrontendPropagateTrace(t *testing.T) {
	var err error

	observedTraceID := make(chan string, 2)

	handler := middleware.Tracer{}.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID, ok := tracing.ExtractTraceID(r.Context())
		if !ok {
			t.Errorf("Request context does not contain trace ID")
		} else {
			observedTraceID <- traceID
		}

		_, err = w.Write([]byte(responseBody))
		require.NoError(t, err)
	}))

	test := func(addr string, _ *Frontend) {
		ctx, sp := tracer.Start(context.Background(), "client")
		defer sp.End()

		traceID := sp.SpanContext().TraceID()
		require.True(t, traceID.IsValid())
		expectedTraceID := traceID.String()

		req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/%s", addr, query), nil)
		require.NoError(t, err)
		req = req.WithContext(ctx)
		err = user.InjectOrgIDIntoHTTPRequest(user.InjectOrgID(ctx, "1"), req)
		require.NoError(t, err)

		client := http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)

		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)

		// Query should do one call.
		assert.Equal(t, expectedTraceID, <-observedTraceID)
	}
	testFrontend(t, defaultFrontendConfig(), handler, test, nil, nil)
}

func TestFrontendCheckReady(t *testing.T) {
	for _, tt := range []struct {
		name             string
		connectedClients int
		msg              string
		readyForRequests bool
	}{
		{"connected clients are ready", 3, "", true},
		{"no url, no clients is not ready", 0, "not ready: number of queriers connected to query-frontend is 0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				MaxOutstandingPerTenant: 5,
				QuerierForgetDelay:      0,
			}

			f, err := New(cfg, limits{}, log.NewNopLogger(), nil)
			require.NoError(t, err)
			require.NoError(t, services.StartAndAwaitRunning(context.Background(), f))
			defer func() {
				require.NoError(t, services.StopAndAwaitTerminated(context.Background(), f))
			}()

			for i := 0; i < tt.connectedClients; i++ {
				querierWorkerConn := queue.NewUnregisteredQuerierWorkerConn(context.Background(), "test")
				require.NoError(t, f.requestQueue.AwaitRegisterQuerierWorkerConn(querierWorkerConn))
			}
			err = f.CheckReady(context.Background())
			errMsg := ""

			if err != nil {
				errMsg = err.Error()
			}

			require.Equal(t, tt.msg, errMsg)
		})
	}
}

// TestFrontendCancel ensures that when client requests are cancelled,
// the underlying query is correctly cancelled _and not retried_.
func TestFrontendCancel(t *testing.T) {
	var tries atomic.Int32
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		tries.Inc()
	})
	test := func(addr string, _ *Frontend) {
		req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/", addr), nil)
		require.NoError(t, err)
		err = user.InjectOrgIDIntoHTTPRequest(user.InjectOrgID(context.Background(), "1"), req)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		req = req.WithContext(ctx)

		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		_, err = http.DefaultClient.Do(req)
		require.Error(t, err)

		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, int32(1), tries.Load())
	}
	testFrontend(t, defaultFrontendConfig(), handler, test, nil, nil)
}

func TestFrontendMetricsCleanup(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("Hello World"))
		require.NoError(t, err)
	})

	reg := prometheus.NewPedanticRegistry()

	test := func(addr string, fr *Frontend) {
		req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/", addr), nil)
		require.NoError(t, err)
		err = user.InjectOrgIDIntoHTTPRequest(user.InjectOrgID(context.Background(), "1"), req)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		assert.Equal(t, "Hello World", string(body))

		require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
				# HELP cortex_query_frontend_queue_length Number of queries in the queue.
				# TYPE cortex_query_frontend_queue_length gauge
				cortex_query_frontend_queue_length{user="1"} 0
			`), "cortex_query_frontend_queue_length"))

		fr.cleanupInactiveUserMetrics("1")

		require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
				# HELP cortex_query_frontend_queue_length Number of queries in the queue.
				# TYPE cortex_query_frontend_queue_length gauge
			`), "cortex_query_frontend_queue_length"))
	}

	testFrontend(t, defaultFrontendConfig(), handler, test, nil, reg)
}

func TestFrontendStats(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := stats.FromContext(r.Context())
		s.AddQueueTime(5 * time.Second)
		w.WriteHeader(200)
	})

	tl := testLogger{}

	test := func(addr string, _ *Frontend) {
		req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/", addr), nil)
		require.NoError(t, err)
		err = user.InjectOrgIDIntoHTTPRequest(user.InjectOrgID(context.Background(), "1"), req)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)

		queryStatsFound := false
		for _, le := range tl.getLogMessages() {
			if le["msg"] == "query stats" {
				require.False(t, queryStatsFound)
				queryStatsFound = true
				require.GreaterOrEqual(t, le["queue_time_seconds"], 5.0)
			}
		}
		require.True(t, queryStatsFound)
	}

	testFrontend(t, defaultFrontendConfig(), handler, test, &tl, nil)
}

type testLogger struct {
	mu          sync.Mutex
	logMessages []map[string]interface{}
}

func (t *testLogger) Log(keyvals ...interface{}) error {
	if len(keyvals)%2 != 0 {
		panic("received uneven number of key/value pairs for log line")
	}

	entryCount := len(keyvals) / 2
	msg := make(map[string]interface{}, entryCount)

	for i := 0; i < entryCount; i++ {
		name := keyvals[2*i].(string)
		value := keyvals[2*i+1]

		msg[name] = value
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.logMessages = append(t.logMessages, msg)
	return nil
}

func (t *testLogger) getLogMessages() []map[string]interface{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]map[string]interface{}(nil), t.logMessages...)
}

func testFrontend(t *testing.T, config Config, handler http.Handler, test func(addr string, frontend *Frontend), l log.Logger, reg prometheus.Registerer) {
	logger := log.NewNopLogger()
	if l != nil {
		logger = l
	}

	var workerConfig querier_worker.Config
	flagext.DefaultValues(&workerConfig)
	workerConfig.MaxConcurrentRequests = 1

	// localhost:0 prevents firewall warnings on Mac OS X.
	grpcListen, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	workerConfig.FrontendAddress = grpcListen.Addr().String()

	httpListen, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)

	v1, err := New(config, limits{}, logger, reg)
	require.NoError(t, err)
	require.NotNil(t, v1)
	require.NoError(t, services.StartAndAwaitRunning(context.Background(), v1))
	defer func() {
		require.NoError(t, services.StopAndAwaitTerminated(context.Background(), v1))
	}()

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	defer grpcServer.GracefulStop()

	frontendv1pb.RegisterFrontendServer(grpcServer, v1)

	// Default HTTP handler config.
	handlerCfg := transport.HandlerConfig{QueryStatsEnabled: true}
	flagext.DefaultValues(&handlerCfg)

	rt := httpgrpcutil.AdaptGrpcRoundTripperToHTTPRoundTripper(v1)
	r := mux.NewRouter()
	r.PathPrefix("/").Handler(middleware.Merge(
		middleware.AuthenticateUser,
		middleware.Tracer{},
	).Wrap(transport.NewHandler(handlerCfg, rt, logger, nil, nil)))

	httpServer := http.Server{
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	defer httpServer.Shutdown(context.Background()) //nolint:errcheck

	go httpServer.Serve(httpListen) //nolint:errcheck
	go grpcServer.Serve(grpcListen) //nolint:errcheck

	var worker services.Service
	worker, err = querier_worker.NewQuerierWorker(workerConfig, httpgrpc_server.NewServer(handler, httpgrpc_server.WithReturn4XXErrors), logger, nil)
	require.NoError(t, err)
	require.NoError(t, services.StartAndAwaitRunning(context.Background(), worker))

	test(httpListen.Addr().String(), v1)

	require.NoError(t, services.StopAndAwaitTerminated(context.Background(), worker))
}

func defaultFrontendConfig() Config {
	config := Config{}
	flagext.DefaultValues(&config)
	return config
}

type limits struct {
	queriers int
}

func (l limits) MaxQueriersPerUser(_ string) int {
	return l.queriers
}
