---
aliases:
  - ../../../operators-guide/monitor-grafana-mimir/dashboards/slow-queries/
  - ../../../operators-guide/monitoring-grafana-mimir/dashboards/slow-queries/
  - ../../../operators-guide/visualizing-metrics/dashboards/slow-queries/
description: Review a description of the Slow queries dashboard.
menuTitle: Slow queries
title: Grafana Mimir Slow queries dashboard
weight: 150
---

<!-- Note: This topic is mounted in the GEM documentation. Ensure that all updates are also applicable to GEM. -->

# Grafana Mimir Slow queries dashboard

The Slow queries dashboard shows details about the slowest queries for a given time range and enables you to filter results by a specific tenant.

If you enable [Grafana Tempo](/oss/tempo/) tracing, the dashboard displays a link to the trace of each query.

This dashboard requires [Grafana Loki](/oss/loki/) to fetch detailed query statistics from logs.

Use this dashboard for the following use cases:

- Identify and analyze the slowest queries in your Mimir cluster.
- Filter data to analyze queries from a specific tenant.
