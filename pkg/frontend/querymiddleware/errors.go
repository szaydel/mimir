// SPDX-License-Identifier: AGPL-3.0-only

package querymiddleware

import (
	"fmt"
	"time"

	apierror "github.com/grafana/mimir/pkg/api/error"
	"github.com/grafana/mimir/pkg/util/globalerror"
	"github.com/grafana/mimir/pkg/util/validation"
)

func newMaxTotalQueryLengthError(actualQueryLen, maxTotalQueryLength time.Duration) error {
	return apierror.New(apierror.TypeBadData, globalerror.MaxTotalQueryLength.MessageWithPerTenantLimitConfig(
		fmt.Sprintf("the total query time range exceeds the limit (query length: %s, limit: %s)", actualQueryLen, maxTotalQueryLength),
		validation.MaxTotalQueryLengthFlag,
	))
}

func newMaxQueryExpressionSizeBytesError(actualSizeBytes, maxQuerySizeBytes int) error {
	return apierror.New(apierror.TypeBadData, globalerror.MaxQueryExpressionSizeBytes.MessageWithPerTenantLimitConfig(
		fmt.Sprintf("the raw query size in bytes exceeds the limit (query size: %d, limit: %d)", actualSizeBytes, maxQuerySizeBytes),
		validation.MaxQueryExpressionSizeBytesFlag,
	))
}

func newQueryBlockedError(administratorReason string) error {
	var reason string
	if administratorReason != "" {
		reason = fmt.Sprintf(" %s", administratorReason)
	}
	return apierror.New(apierror.TypeBadData, globalerror.QueryBlocked.Message(fmt.Sprintf("the request has been blocked by the cluster administrator%s", reason)))
}

func newQueryLimitedError(allowedFrequency time.Duration, tenantID string) error {
	return apierror.New(
		apierror.TypeTooManyRequests, globalerror.QueryLimited.Message(
			fmt.Sprintf("the query has been limited by the cluster administrator, and is being run more frequently than the allowed frequency %s against tenant %s", allowedFrequency, tenantID),
		))
}
