{
  local container = $.core.v1.container,

  ruler_args::
    $._config.commonConfig +
    $._config.usageStatsConfig +
    $._config.grpcConfig +
    $._config.storageConfig +
    $._config.blocksStorageConfig +
    $._config.rulerStorageConfig +
    $._config.queryConfig +
    $._config.queryEngineConfig +
    $._config.ingesterRingClientConfig +
    $._config.rulerLimitsConfig +
    $._config.queryBlocksStorageConfig +
    $.blocks_metadata_caching_config +
    $.ruler_storage_caching_config +
    $.bucket_index_config
    {
      target: 'ruler',

      // File path used to store temporary rule files loaded by the Prometheus rule managers.
      'ruler.rule-path': '/rules',

      // Alertmanager configs
      'ruler.alertmanager-url': 'http://alertmanager.%(namespace)s.svc.%(cluster_domain)s/alertmanager' % $._config,

      // Ring Configs
      'ruler.ring.store': 'consul',
      'ruler.ring.consul.hostname': 'consul.%(namespace)s.svc.%(cluster_domain)s:8500' % $._config,

      'server.http-listen-port': $._config.server_http_port,

      // Increased from 2s to 10s in order to accommodate writing large rule results ot he ingester.
      'distributor.remote-timeout': '10s',
    } + if $._config.ingest_storage_enabled then {
      // Set the max buffered bytes in the Kafka client used by the ruler based on the expected max rule evaluation response size,
      // clamping it between 1 GB (default) and 4 GB.
      'ingest-storage.kafka.producer-max-buffered-bytes': std.clamp(10 * $._config.ruler_remote_evaluation_max_query_response_size_bytes, 1024 * 1024 * 1024, 4 * 1024 * 1024 * 1024),
    } else {},

  ruler_env_map:: {},

  ruler_node_affinity_matchers:: [],

  ruler_container::
    if $._config.ruler_enabled then
      container.new('ruler', $._images.ruler) +
      container.withPorts($.util.defaultPorts) +
      container.withArgsMixin($.util.mapToFlags($.ruler_args)) +
      (if std.length($.ruler_env_map) > 0 then container.withEnvMap(std.prune($.ruler_env_map)) else {}) +
      $.util.resourcesRequests('1', '6Gi') +
      $.util.resourcesLimits('16', '16Gi') +
      $.util.readinessProbe +
      $.tracing_env_mixin
    else {},

  local deployment = $.apps.v1.deployment,

  ruler_deployment: if !$._config.ruler_enabled then null else
    local name = 'ruler';

    deployment.new(name, 2, [$.ruler_container]) +
    $.newMimirNodeAffinityMatchers($.ruler_node_affinity_matchers) +
    (if !std.isObject($._config.node_selector) then {} else deployment.mixin.spec.template.spec.withNodeSelectorMixin($._config.node_selector)) +
    deployment.mixin.spec.strategy.rollingUpdate.withMaxSurge('50%') +
    deployment.mixin.spec.strategy.rollingUpdate.withMaxUnavailable(0) +
    deployment.mixin.spec.template.spec.withTerminationGracePeriodSeconds(600) +
    $.newMimirSpreadTopology(name, $._config.ruler_querier_topology_spread_max_skew) +
    $.mimirVolumeMounts,

  ruler_service: if !$._config.ruler_enabled then null else
    $.util.serviceFor($.ruler_deployment, $._config.service_ignored_labels),

  ruler_pdb: if !$._config.ruler_enabled then null else
    $.newMimirPdb('ruler'),
}
