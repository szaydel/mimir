// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/util/log/log.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package log

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	dslog "github.com/grafana/dskit/log"
	"github.com/grafana/dskit/spanlogger"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Logger is a shared go-kit logger.
	// TODO: Change all components to take a non-global logger via their constructors.
	// Prefer accepting a non-global logger as an argument.
	Logger         = log.NewNopLogger()
	bufferedLogger *dslog.BufferedLogger
)

type RateLimitedLoggerCfg struct {
	Enabled       bool
	LogsPerSecond float64
	LogsBurstSize int
	Registry      prometheus.Registerer
}

// InitLogger initialises the global gokit logger (util_log.Logger) and returns that logger.
func InitLogger(logFormat string, logLevel dslog.Level, buffered bool, rateLimitedCfg RateLimitedLoggerCfg) log.Logger {
	writer := getWriter(buffered)
	logger := dslog.NewGoKitWithWriter(logFormat, writer)

	if rateLimitedCfg.Enabled {
		// use UTC timestamps and skip 7 stack frames if rate limited logger is needed.
		logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", spanlogger.Caller(7))
		logger = dslog.NewRateLimitedLogger(logger, rateLimitedCfg.LogsPerSecond, rateLimitedCfg.LogsBurstSize, rateLimitedCfg.Registry)
	} else {
		// use UTC timestamps and skip 6 stack frames if no rate limited logger is needed.
		logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", spanlogger.Caller(6))
	}
	// Must put the level filter last for efficiency.
	logger = newFilter(logger, logLevel)

	// Set global logger.
	Logger = logger
	return logger
}

// MakeLeveledLogger makes a logger wrapped with the same level filter used in the global logger.
// Intended for use in tests that don't set the global logger.
func MakeLeveledLogger(writer io.Writer, level string) log.Logger {
	var logLevel dslog.Level
	if err := logLevel.Set(level); err != nil {
		panic(err)
	}
	logger := log.NewLogfmtLogger(writer)
	return newFilter(logger, logLevel)
}

type logLevel int

const (
	debugLevel logLevel = iota
	infoLevel
	warnLevel
	errorLevel
)

type privateLevelDetector struct {
	string
	logLevel
}

func (x *privateLevelDetector) String() string {
	return x.string
}

var _ log.Logger = levelFilter{}

// Pass through Logger and implement the DebugEnabled interface that spanlogger looks for.
type levelFilter struct {
	log.Logger
	lvl logLevel
}

func newFilter(logger log.Logger, lvl dslog.Level) log.Logger {
	var l logLevel
	switch lvl.String() {
	case "info":
		l = infoLevel
	case "warn":
		l = warnLevel
	case "error":
		l = errorLevel
	default:
		l = debugLevel
	}
	return &levelFilter{
		Logger: level.NewFilter(logger, lvl.Option),
		lvl:    l,
	}
}

// If we are called with a special magic struct, use it to pass back the logging level.
func (f levelFilter) Log(keyvals ...interface{}) error {
	if len(keyvals) > 0 {
		if x, ok := keyvals[len(keyvals)-1].(*privateLevelDetector); ok {
			x.logLevel = f.lvl
			return nil
		}
	}
	return f.Logger.Log(keyvals...)
}

func (f *levelFilter) DebugEnabled() bool {
	return f.lvl <= debugLevel
}

func getWriter(buffered bool) io.Writer {
	writer := os.Stderr

	if buffered {
		var (
			logEntries    uint32 = 256                    // buffer up to 256 log lines in memory before flushing to a write(2) syscall
			logBufferSize uint32 = 10e6                   // 10MB
			flushTimeout         = 100 * time.Millisecond // flush the buffer after 100ms regardless of how full it is, to prevent losing many logs in case of ungraceful termination
		)

		// retain a reference to this logger because it doesn't conform to the standard Logger interface,
		// and we can't unwrap it to get the underlying logger when we flush on shutdown
		bufferedLogger = dslog.NewBufferedLogger(writer, logEntries,
			dslog.WithFlushPeriod(flushTimeout),
			dslog.WithPrellocatedBuffer(logBufferSize),
		)

		return bufferedLogger
	}
	return log.NewSyncWriter(writer)
}

// CheckFatal prints an error and exits with error code 1 if err is non-nil
func CheckFatal(location string, err error) {
	if err != nil {
		logger := level.Error(Logger)
		if location != "" {
			logger = log.With(logger, "msg", "error "+location)
		}
		// %+v gets the stack trace from errors using github.com/pkg/errors
		logger.Log("err", fmt.Sprintf("%+v", err))

		if err = Flush(); err != nil {
			fmt.Fprintln(os.Stderr, "Could not flush logger", err)
		}
		os.Exit(1)
	}
}

// Flush forces the buffered logger, if configured, to flush to the underlying writer
// This is typically only called when the application is shutting down.
func Flush() error {
	if bufferedLogger != nil {
		return bufferedLogger.Flush()
	}

	return nil
}
