# Plan: YAML Config + Per-App Env Config + Slack Routing

## Overview

Replace env vars with a layered YAML config approach:

1. **`config.yaml`** — global settings (workers, debounce, denylist, bot token, etc.)
2. **`envs/*.yaml`** — one per environment, defines cluster → app → namespaces
3. **`slack-routing.yaml`** — app name → Slack channel mapping

## Config Structure

### Global config (`config.yaml`)
```yaml
kubeconfigDir: /etc/kubeconfigs
envConfigDir: /etc/rollout-monitor/envs
slackRoutingFile: /etc/rollout-monitor/slack-routing.yaml
dispatchMode: slack
slackBotToken: ""  # override via Secret env var
namespaceDenylist: [kube-system, kube-public, kube-node-lease]
workerCount: 3
debounceSeconds: 30
persistenceEnabled: true
```

### Env config (`envs/prod.yaml`)
```yaml
clusters:
  - name: prod-us-east
    deployments:
      - app: payments-service
        namespaces: [payments, payments-jobs]
      - app: checkout-api
        namespaces: [checkout]
  - name: prod-eu-west
    deployments:
      - app: payments-service
        namespaces: [payments]
```

### Slack routing (`slack-routing.yaml`)
```yaml
payments-service: "#payments-deploys"
checkout-api: "#checkout-deploys"
```

## Data Flow

```
config.yaml ──→ Config loader ──→ global settings
envs/*.yaml ──→ EnvConfig loader ──→ NamespaceLookup (cluster+ns → app)
slack-routing.yaml ──→ SlackRouting (app → channel)

ClusterWatcher → RolloutEvent → eventEnricher(event) → sets App + SlackChannel
                                                         ↓
                                              SlackBotTarget → posts to correct channel
```

## Helm Integration

- `values.yaml` — base defaults
- `values-prod.yaml` / `values-staging.yaml` — per-env overrides
- Helm renders env configs into a ConfigMap mounted as directory
- Sensitive values (bot token, webhook URL) via Secret + env var override

## Changes Made

| Action | File | Description |
|--------|------|-------------|
| Modify | `internal/config/config.go` | YAML-based config with env var fallback, added SlackBotToken/EnvConfigDir/SlackRoutingFile |
| New | `internal/config/env_config.go` | EnvConfig/SlackRouting types, loaders, NamespaceLookup |
| Modify | `internal/models/event.go` | Added App + SlackChannel fields to RolloutEvent |
| New | `internal/dispatch/slack_bot.go` | SlackBotTarget using Slack Web API with per-event channel routing |
| Modify | `internal/watcher/informer.go` | Added eventEnricher callback, populates App+SlackChannel on events |
| Modify | `internal/watcher/manager.go` | Added SetEventEnricher, passes enricher to watchers |
| Modify | `cmd/monitor/main.go` | Loads env configs + slack routing, wires enricher + SlackBotTarget |
