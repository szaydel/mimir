---
aliases:
  - ../../operators-guide/tools/mimir-continuous-test/
description: Use mimir-continuous-test to continuously run smoke tests on live Grafana Mimir clusters.
menuTitle: Mimir-continuous-test
title: Grafana mimir-continuous-test
weight: 30
---

<!-- Note: This topic is mounted in the GEM documentation. Ensure that all updates are also applicable to GEM. -->

# Grafana mimir-continuous-test

As a developer, you can use the mimir-continuous-test tool to run smoke tests on live Grafana Mimir clusters.
This tool identifies a class of bugs that could be difficult to spot during development.
Two operating modes are supported:

- As a continuously running deployment in your environment, mimir-continuous-test can be used to detect issues on a live Grafana Mimir cluster over time.
- As an ad-hoc smoke test tool, mimir-continuous-test can be used to validate basic functionality after configuration changes are made to a Grafana Mimir cluster.

## Download and run mimir-continuous-test

- Using Docker:

```bash
docker pull "grafana/mimir:latest"
docker run --rm -ti grafana/mimir -target=continuous-test
```

- Using a local binary:

Download the appropriate [mimir binary](https://github.com/grafana/mimir/releases/latest) for your operating system and architecture, and make it executable.

For Linux with the AMD64 architecture, execute the following command:

```bash
curl -Lo mimir https://github.com/grafana/mimir/releases/latest/download/mimir-linux-amd64
chmod +x mimir
mimir -target=continuous-test
```

{{< admonition type="note" >}}
In [Grafana Mimir 2.13](../../../release-notes/v2.13/) the mimir-continuous-test became a part of Mimir as its own target.

The standalone `grafana/mimir-continuous-test` Docker image and mimir-continuous-test binary are now deprecated but available for backwards compatibility.
{{< /admonition >}}

## Configure mimir-continuous-test

Mimir-continuous-test requires the endpoints of the backend Grafana Mimir clusters and the authentication for writing and querying testing metrics:

- Set `-tests.write-endpoint` to the base endpoint on the write path. Remove any trailing slash from the URL. The tool appends the specific API path to the URL, for example `/api/v1/push` for the remote-write API.
- Set `-tests.read-endpoint` to the base endpoint on the read path. Remove any trailing slash from the URL. The tool appends the specific API path to the URL, for example `/api/v1/query_range` for the range-query API.
- Set the authentication means to use to write and read metrics in tests. By priority order:
  - `-tests.bearer-token` for bearer token authentication.
  - `-tests.basic-auth-user` and `-tests.basic-auth-password` for a basic authentication.
  - `-tests.tenant-id` to the tenant ID, default to `anonymous`.
- Set `-tests.smoke-test` to run the test once and immediately exit. In this mode, the process exit code is non-zero when the test fails.

{{< admonition type="note" >}}
You can run `mimir -help` to list all available configuration options. All configuration options for mimir-continuous-test begin with `tests`.
{{< /admonition >}}

## How it works

Mimir-continuous-test periodically runs a suite of tests, writes data to Mimir, queries that data back, and checks if the query results match what is expected.
The tool exposes metrics that you can use to alert on test failures. The tool logs the details about the failed tests.

### Exported metrics

Mimir-continuous-test exposes the following Prometheus metrics at the `/metrics` endpoint listening on the port that you configured via the flag `-server.metrics-port`:

```bash
# HELP mimir_continuous_test_writes_total Total number of attempted write requests.
# TYPE mimir_continuous_test_writes_total counter
mimir_continuous_test_writes_total{test="<name>"}
{test="<name>"}

# HELP mimir_continuous_test_writes_failed_total Total number of failed write requests.
# TYPE mimir_continuous_test_writes_failed_total counter
mimir_continuous_test_writes_failed_total{test="<name>",status_code="<code>"}

# HELP mimir_continuous_test_writes_request_duration_seconds Duration of the requests
# TYPE mimir_continuous_test_writes_request_duration_seconds histogram
mimir_continuous_test_writes_request_duration_seconds{test="<name>"}

# HELP mimir_continuous_test_queries_total Total number of attempted query requests.
# TYPE mimir_continuous_test_queries_total counter
mimir_continuous_test_queries_total{test="<name>"}

# HELP mimir_continuous_test_queries_failed_total Total number of failed query requests.
# TYPE mimir_continuous_test_queries_failed_total counter
mimir_continuous_test_queries_failed_total{test="<name>"}

# HELP mimir_continuous_test_query_result_checks_total Total number of query results checked for correctness.
# TYPE mimir_continuous_test_query_result_checks_total counter
mimir_continuous_test_query_result_checks_total{test="<name>"}

# HELP mimir_continuous_test_query_result_checks_failed_total Total number of query results failed when checking for correctness.
# TYPE mimir_continuous_test_query_result_checks_failed_total counter
mimir_continuous_test_query_result_checks_failed_total{test="<name>"}

# HELP mimir_continuous_test_queries_request_duration_seconds Duration of the requests
# TYPE mimir_continuous_test_queries_request_duration_seconds histogram
mimir_continuous_test_queries_request_duration_seconds{results_cache="false",test="<name>"}
mimir_continuous_test_queries_request_duration_seconds{results_cache="true",test="<name>"}
```

{{< admonition type="note" >}}
The metrics `mimir_continuous_test_writes_request_duration_seconds` and `mimir_continuous_test_queries_request_duration_seconds`
are exposed as [native histograms](../../../send/native-histograms/) and don’t have proper textual presentation on the `/metrics` endpoint.
{{< /admonition >}}

### Alerts

[Grafana Mimir alerts](../../monitor-grafana-mimir/installing-dashboards-and-alerts/) include checks on failures that mimir-continuous-test tracks.
When running mimir-continuous-test, use the provided alerts.
