// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/util/allowed_tenants_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllowList_NoConfig(t *testing.T) {
	a := NewAllowList(nil, nil)
	require.True(t, a.IsAllowed("all"))
	require.True(t, a.IsAllowed("tenants"))
	require.True(t, a.IsAllowed("allowed"))
}

func TestAllowList_Enabled(t *testing.T) {
	a := NewAllowList([]string{"A", "B"}, nil)
	require.True(t, a.IsAllowed("A"))
	require.True(t, a.IsAllowed("B"))
	require.False(t, a.IsAllowed("C"))
	require.False(t, a.IsAllowed("D"))
}

func TestAllowList_Disabled(t *testing.T) {
	a := NewAllowList(nil, []string{"A", "B"})
	require.False(t, a.IsAllowed("A"))
	require.False(t, a.IsAllowed("B"))
	require.True(t, a.IsAllowed("C"))
	require.True(t, a.IsAllowed("D"))
}

func TestAllowList_Combination(t *testing.T) {
	a := NewAllowList([]string{"A", "B"}, []string{"B", "C"})
	require.True(t, a.IsAllowed("A"))  // enabled, and not disabled
	require.False(t, a.IsAllowed("B")) // enabled, but also disabled
	require.False(t, a.IsAllowed("C")) // disabled
	require.False(t, a.IsAllowed("D")) // not enabled
}

func TestAllowList_Nil(t *testing.T) {
	var a *AllowList

	// All tenants are allowed when using nil as allowed tenants.
	require.True(t, a.IsAllowed("A"))
	require.True(t, a.IsAllowed("B"))
	require.True(t, a.IsAllowed("C"))
}
