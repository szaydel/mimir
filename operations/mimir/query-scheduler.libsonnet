// Query-scheduler is optional service. When query-scheduler.libsonnet is added to Mimir, querier and frontend
// are reconfigured to use query-scheduler service.
{
  local container = $.core.v1.container,
  local deployment = $.apps.v1.deployment,

  query_scheduler_args+::
    $._config.commonConfig +
    $._config.usageStatsConfig +
    $._config.grpcConfig +
    $._config.querySchedulerRingLifecyclerConfig
    {
      target: 'query-scheduler',
      'server.http-listen-port': $._config.server_http_port,
      'query-scheduler.max-outstanding-requests-per-tenant': 100,
    } + (
      if $._config.query_scheduler_service_discovery_mode != 'ring' then {} else {
        'query-scheduler.service-discovery-mode': 'ring',
      }
    ),

  query_scheduler_ports:: $.util.defaultPorts,

  newQuerySchedulerContainer(name, args, envmap={})::
    container.new(name, $._images.query_scheduler) +
    container.withPorts($.query_scheduler_ports) +
    container.withArgsMixin($.util.mapToFlags(args)) +
    (if std.length(envmap) > 0 then container.withEnvMap(std.prune(envmap)) else {}) +
    $.tracing_env_mixin +
    $.util.readinessProbe +
    $.util.resourcesRequests('2', '1Gi') +
    $.util.resourcesLimits(null, '2Gi'),

  query_scheduler_env_map:: {},

  query_scheduler_node_affinity_matchers:: [],

  query_scheduler_container::
    self.newQuerySchedulerContainer('query-scheduler', $.query_scheduler_args, $.query_scheduler_env_map),

  newQuerySchedulerDeployment(name, container, nodeAffinityMatchers=[])::
    deployment.new(name, 2, [container]) +
    $.newMimirNodeAffinityMatchers(nodeAffinityMatchers) +
    $.mimirVolumeMounts +
    (if !std.isObject($._config.node_selector) then {} else deployment.mixin.spec.template.spec.withNodeSelectorMixin($._config.node_selector)) +
    $.util.antiAffinity +
    // Do not run more query-schedulers than expected.
    deployment.mixin.spec.strategy.rollingUpdate.withMaxSurge(1) +
    deployment.mixin.spec.strategy.rollingUpdate.withMaxUnavailable(0) +
    // Set a termination grace period greater than query timeout.
    deployment.mixin.spec.template.spec.withTerminationGracePeriodSeconds(180),

  query_scheduler_deployment: if !$._config.query_scheduler_enabled then {} else
    self.newQuerySchedulerDeployment('query-scheduler', $.query_scheduler_container, $.query_scheduler_node_affinity_matchers),

  query_scheduler_service: if !$._config.query_scheduler_enabled then {} else
    $.util.serviceFor($.query_scheduler_deployment, $._config.service_ignored_labels),

  local discoveryServiceName(prefix) = '%s-discovery' % prefix,

  // Headless to make sure resolution gets IP address of target pods, and not service IP.
  newQuerySchedulerDiscoveryService(name, deployment)::
    $.newMimirDiscoveryService(discoveryServiceName(name), deployment),

  query_scheduler_discovery_service: if !$._config.query_scheduler_enabled then {} else
    self.newQuerySchedulerDiscoveryService('query-scheduler', $.query_scheduler_deployment),

  query_scheduler_pdb: if !$._config.query_scheduler_enabled then null else
    $.newMimirPdb('query-scheduler'),

  // Reconfigure querier and query-frontend to use scheduler.

  local querySchedulerAddress(name) =
    '%s.%s.svc.%s:9095' % [discoveryServiceName(name), $._config.namespace, $._config.cluster_domain],

  querierUseQuerySchedulerArgs(name):: {
    'querier.frontend-address': null,
  } + (
    if $._config.query_scheduler_service_discovery_mode == 'ring' && $._config.query_scheduler_service_discovery_ring_read_path_enabled then {
      'query-scheduler.service-discovery-mode': 'ring',
      'query-scheduler.ring.prefix': if name == 'query-scheduler' then '' else '%s/' % name,
    } else {
      'querier.scheduler-address': querySchedulerAddress(name),
    }
  ),

  queryFrontendUseQuerySchedulerArgs(name)::
    if $._config.query_scheduler_service_discovery_mode == 'ring' && $._config.query_scheduler_service_discovery_ring_read_path_enabled then {
      'query-scheduler.service-discovery-mode': 'ring',
      'query-scheduler.ring.prefix': if name == 'query-scheduler' then '' else '%s/' % name,
    } else {
      'query-frontend.scheduler-address': querySchedulerAddress(name),
    },

  querier_args+:: if !$._config.query_scheduler_enabled then {} else
    self.querierUseQuerySchedulerArgs('query-scheduler'),

  query_frontend_args+:: if !$._config.query_scheduler_enabled then {} else
    self.queryFrontendUseQuerySchedulerArgs('query-scheduler'),
}
