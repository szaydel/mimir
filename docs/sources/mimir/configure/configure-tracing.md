---
aliases:
  - ../configuring/configuring-tracing/
  - configuring-tracing/
  - ../operators-guide/configure/configure-tracing/
description: Learn how to configure Grafana Mimir to send traces to Jaeger.
menuTitle: Tracing
title: Configure Grafana Mimir tracing
weight: 100
---

# Configure Grafana Mimir tracing

Distributed tracing is a valuable tool for troubleshooting the behavior of Grafana Mimir in production.

Grafana Mimir is transitioning from [Jaeger](https://www.jaegertracing.io/) to [OpenTelemetry](https://opentelemetry.io/docs/languages/go/getting-started/) to implement distributed tracing.
During this transition, Mimir uses OTel libraries, but performs the configuration using Jaeger environment variables.

## Dependencies

Set up a Jaeger deployment to collect and store traces from Grafana Mimir.
A deployment includes either the Jaeger all-in-one binary or a distributed system of agents, collectors, and queriers.
If you run Grafana Mimir on Kubernetes, refer to [Jaeger Kubernetes](https://github.com/jaegertracing/jaeger-kubernetes).

## Configuration

To configure Grafana Mimir to send traces, perform the following steps:

1. Set the `JAEGER_AGENT_HOST` environment variable in all components to point to the Jaeger agent.
1. Enable sampling in the appropriate components:
   - The ingester and ruler self-initiate traces. You should have sampling explicitly enabled.
   - You can enable sampling for the distributor and query-frontend in Grafana Mimir or in an upstream service, like a proxy or gateway running in front of Grafana Mimir.

To enable sampling in Grafana Mimir components, you can specify either `JAEGER_SAMPLER_MANAGER_HOST_PORT` for remote sampling, or `JAEGER_SAMPLER_TYPE` and `JAEGER_SAMPLER_PARAM` to manually set sampling configuration.
Refer to [Jaeger Client Go documentation](https://github.com/jaegertracing/jaeger-client-go#environment-variables)for the full list of environment variables you can configure.

{{< admonition type="note" >}}
You must specify one of `JAEGER_AGENT_HOST` or `JAEGER_SAMPLER_MANAGER_HOST_PORT` in each component for Jaeger to be enabled, even if you plan to use the default values.
{{< /admonition >}}
