---
aliases:
  - ../../../operators-guide/monitor-grafana-mimir/dashboards/ruler/
  - ../../../operators-guide/monitoring-grafana-mimir/dashboards/ruler/
  - ../../../operators-guide/visualizing-metrics/dashboards/ruler/
description: View an example Ruler dashboard.
menuTitle: Ruler
title: Grafana Mimir Ruler dashboard
weight: 130
---

<!-- Note: This topic is mounted in the GEM documentation. Ensure that all updates are also applicable to GEM. -->

# Grafana Mimir Ruler dashboard

The Ruler dashboard shows health and activity metrics for the ruler and object storage metrics for operations triggered by the ruler.

Use this dashboard for the following use cases:

- Monitor rule evaluation performance.
- Track the operational status of the ruler component.
- Monitor the ruler's interactions with object storage.
- Track the operational status of the ruler component, ensuring it's functioning correctly and evaluating rules as expected.

## Example

The following example shows a Ruler dashboard from a demo cluster.

![Grafana Mimir ruler dashboard](mimir-ruler.png)

{{< admonition type="note" >}}
Even while operating in [Remote ruler mode](../../../../references/architecture/components/ruler/#remote) there are still values for the `Read from ingesters - QPS`.
{{< /admonition >}}

This is because the metrics are inclusive of intermediate services and are showing the requests that ultimately reach the ingesters.

For a more detailed view of the read path when using remote ruler mode, see the [Remote ruler reads](../remote-ruler-reads-resources/) dashboard.
