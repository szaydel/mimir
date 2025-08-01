// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/distributor/ha_tracker_http.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package distributor

import (
	"cmp"
	_ "embed" // Used to embed html template
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/timestamp"

	"github.com/grafana/mimir/pkg/util"
)

//go:embed ha_tracker_status.gohtml
var haTrackerStatusPageHTML string
var haTrackerStatusPageTemplate = template.Must(template.New("ha-tracker").Parse(haTrackerStatusPageHTML))

type haTrackerStatusPageContents struct {
	Elected []haTrackerReplica `json:"elected"`
	Now     time.Time          `json:"now"`
}

type haTrackerReplica struct {
	UserID              string        `json:"userID"`
	Cluster             string        `json:"cluster"`
	Replica             string        `json:"replica"`
	LastElectionTime    time.Time     `json:"lastElectionTime"`
	ElectedLastSeenTime time.Time     `json:"electedLastSeenTime"`
	UpdateTime          time.Duration `json:"updateDuration"`
	FailoverTime        time.Duration `json:"failoverDuration"`
}

func (h *defaultHaTracker) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h.electedLock.RLock()

	var electedReplicas []haTrackerReplica
	for userID, clusters := range h.clusters {
		uh := h.forUser(userID)
		for cluster, entry := range clusters {
			desc := &entry.elected
			electedReplicas = append(electedReplicas, haTrackerReplica{
				UserID:              userID,
				Cluster:             cluster,
				Replica:             desc.Replica,
				LastElectionTime:    timestamp.Time(desc.ElectedAt),
				ElectedLastSeenTime: timestamp.Time(desc.ReceivedAt),
				UpdateTime:          time.Until(timestamp.Time(desc.ReceivedAt).Add(uh.updateTimeout)),
				FailoverTime:        time.Until(timestamp.Time(desc.ReceivedAt).Add(uh.failoverTimeout)),
			})
		}
	}
	h.electedLock.RUnlock()

	slices.SortFunc(electedReplicas, func(a, b haTrackerReplica) int {
		return cmp.Or(
			strings.Compare(a.UserID, b.UserID),
			strings.Compare(a.Cluster, b.Cluster),
		)
	})

	util.RenderHTTPResponse(w, haTrackerStatusPageContents{
		Elected: electedReplicas,
		Now:     time.Now(),
	}, haTrackerStatusPageTemplate, req)
}
