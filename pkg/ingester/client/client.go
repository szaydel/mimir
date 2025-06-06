// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/ingester/client/client.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package client

import (
	"flag"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/grpcclient"
	"github.com/grafana/dskit/middleware"
	"github.com/grafana/dskit/ring"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/grafana/mimir/pkg/mimirpb"
	querierapi "github.com/grafana/mimir/pkg/querier/api"
	"github.com/grafana/mimir/pkg/util"
	"github.com/grafana/mimir/pkg/util/grpcencoding/s2"
)

// HealthAndIngesterClient is the union of IngesterClient and grpc_health_v1.HealthClient.
type HealthAndIngesterClient interface {
	IngesterClient
	grpc_health_v1.HealthClient
	Close() error
}

type closableHealthAndIngesterClient struct {
	IngesterClient
	grpc_health_v1.HealthClient
	conn *grpc.ClientConn
}

// MakeIngesterClient makes a new IngesterClient
func MakeIngesterClient(inst ring.InstanceDesc, cfg Config, metrics *Metrics, logger log.Logger) (HealthAndIngesterClient, error) {
	reportGRPCStatusesOptions := []middleware.InstrumentationOption{middleware.ReportGRPCStatusOption}
	unary, stream := grpcclient.Instrument(metrics.requestDuration, reportGRPCStatusesOptions...)
	unary = append(unary, querierapi.ReadConsistencyClientUnaryInterceptor)
	stream = append(stream, querierapi.ReadConsistencyClientStreamInterceptor)

	opts, err := cfg.GRPCClientConfig.DialOption(unary, stream, util.NewInvalidClusterValidationReporter(cfg.GRPCClientConfig.ClusterValidation.Label, metrics.invalidClusterVerificationLabels, logger))
	if err != nil {
		return nil, err
	}
	opts = append(opts, grpc.WithStatsHandler(otelgrpc.NewClientHandler()))

	// nolint:staticcheck // grpc.Dial() has been deprecated; we'll address it before upgrading to gRPC 2.
	conn, err := grpc.Dial(inst.Addr, opts...)
	if err != nil {
		return nil, err
	}

	ingClient := NewIngesterClient(conn)
	ingClient = newBufferPoolingIngesterClient(ingClient, conn)

	return &closableHealthAndIngesterClient{
		IngesterClient: ingClient,
		HealthClient:   grpc_health_v1.NewHealthClient(conn),
		conn:           conn,
	}, nil
}

func (c *closableHealthAndIngesterClient) Close() error {
	return c.conn.Close()
}

// Config is the configuration struct for the ingester client
type Config struct {
	GRPCClientConfig grpcclient.Config `yaml:"grpc_client_config" doc:"description=Configures the gRPC client used to communicate with ingesters from distributors, queriers and rulers."`
}

// RegisterFlags registers configuration settings used by the ingester client config.
func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	cfg.RegisterFlagsWithPrefix("ingester.client", f)
}

func (cfg *Config) RegisterFlagsWithPrefix(prefix string, f *flag.FlagSet) {
	cfg.GRPCClientConfig.CustomCompressors = []string{s2.Name}
	cfg.GRPCClientConfig.RegisterFlagsWithPrefix(prefix, f)
}

func (cfg *Config) Validate() error {
	return cfg.GRPCClientConfig.Validate()
}

type CombinedQueryStreamResponse struct {
	Chunkseries     []TimeSeriesChunk
	Timeseries      []mimirpb.TimeSeries
	StreamingSeries []StreamingSeries
	StreamReaders   []*SeriesChunksStreamReader
}
