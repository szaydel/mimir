// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/alertmanager/state_persister_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package alertmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/services"
	"github.com/prometheus/alertmanager/cluster/clusterpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/grafana/mimir/pkg/alertmanager/alertspb"
	"github.com/grafana/mimir/pkg/alertmanager/alertstore"
)

type fakePersistableState struct {
	PersistableState

	position int
	readyc   chan struct{}

	getResult *clusterpb.FullState
	getError  error
}

func (f *fakePersistableState) Position() int {
	return f.position
}

func (f *fakePersistableState) GetFullState() (*clusterpb.FullState, error) {
	return f.getResult, f.getError
}

func newFakePersistableState() *fakePersistableState {
	return &fakePersistableState{
		readyc: make(chan struct{}),
	}
}

func (f *fakePersistableState) WaitReady(context.Context) error {
	<-f.readyc
	return nil
}

type fakeStoreWrite struct {
	user string
	desc alertspb.FullStateDesc
}

type fakeStore struct {
	alertstore.AlertStore

	writesMtx sync.Mutex
	writes    []fakeStoreWrite
}

func (f *fakeStore) SetFullState(_ context.Context, user string, desc alertspb.FullStateDesc) error {
	f.writesMtx.Lock()
	defer f.writesMtx.Unlock()
	f.writes = append(f.writes, fakeStoreWrite{user, desc})
	return nil
}

func (f *fakeStore) getWrites() []fakeStoreWrite {
	f.writesMtx.Lock()
	defer f.writesMtx.Unlock()
	return f.writes
}

func makeTestFullState() *clusterpb.FullState {
	return &clusterpb.FullState{
		Parts: []clusterpb.Part{
			{
				Key:  "key",
				Data: []byte("data"),
			},
		},
	}
}

func makeTestStatePersister(t *testing.T, position int, userID string) (*fakePersistableState, *fakeStore, *statePersister) {
	state := newFakePersistableState()
	state.position = position
	store := &fakeStore{}
	cfg := PersisterConfig{Interval: 1 * time.Second}

	s := newStatePersister(cfg, userID, state, store, log.NewNopLogger(), nil)

	require.NoError(t, s.StartAsync(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, services.StopAndAwaitTerminated(context.Background(), s))
	})

	return state, store, s
}

func TestStatePersister_Position0ShouldWrite(t *testing.T) {
	userID := "user-1"
	state, store, s := makeTestStatePersister(t, 0, userID)

	// Should not start until the state becomes ready.
	{
		time.Sleep(5 * time.Second)

		assert.Equal(t, services.Starting, s.State())
		assert.Equal(t, 0, len(store.getWrites()))
	}

	// Should start successfully once the state returns from WaitReady.
	{
		state.getResult = makeTestFullState()
		close(state.readyc)

		assert.NoError(t, s.AwaitRunning(context.Background()))
	}

	// Should receive a write to the store.
	{
		var storeWrites []fakeStoreWrite
		require.Eventually(t, func() bool {
			storeWrites = store.getWrites()
			return len(storeWrites) == 1
		}, 5*time.Second, 100*time.Millisecond)

		expectedDesc := alertspb.FullStateDesc{
			State: makeTestFullState(),
		}

		assert.Equal(t, userID, storeWrites[0].user)
		assert.Equal(t, expectedDesc, storeWrites[0].desc)
	}
}

func TestStatePersister_Position1ShouldNotWrite(t *testing.T) {
	state, store, s := makeTestStatePersister(t, 1, "x")

	// Start the persister.
	{
		require.Equal(t, services.Starting, s.State())

		state.getResult = makeTestFullState()
		close(state.readyc)

		require.NoError(t, s.AwaitRunning(context.Background()))
		require.Equal(t, services.Running, s.State())
	}

	// Should not have stored anything, having passed the interval multiple times.
	{
		time.Sleep(5 * time.Second)

		assert.Equal(t, 0, len(store.getWrites()))
	}
}
