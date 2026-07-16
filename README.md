# Kubernetes Deployment Monitor Operator

A Kubernetes Operator built in Go that monitors deployment rollouts, persists state via Custom Resource Definitions (CRDs), and dispatches notifications to Holmes and Slack.

## Overview

The k8s-deploy-monitor-operator provides automated monitoring and alerting for Kubernetes deployment rollouts. It leverages Kubernetes' native CRD (Custom Resource Definition) mechanism to persistently track deployment status and integrates with external notification systems for real-time alerts.

## Features

- **Deployment Rollout Monitoring**: Automatically tracks and monitors Kubernetes deployment rollout progress
- **CRD Persistence**: Uses Custom Resource Definitions to store and manage deployment monitoring state
- **Multi-Channel Notifications**: 
  - Holmes integration for enterprise alerting
  - Slack notifications for team visibility
- **Native Kubernetes Integration**: Built as a Kubernetes Operator following standard patterns

## Prerequisites

- Kubernetes cluster (v1.16+)
- kubectl configured to access your cluster
- Go 1.16+ (for building from source)

## Installation

### From Pre-built Images

```bash
# Apply the operator deployment to your cluster
kubectl apply -f config/manager/manager.yaml
```

### From Source

```bash
# Clone the repository
git clone https://github.com/koolhandluke/k8s-deploy-monitor-operator.git
cd k8s-deploy-monitor-operator

# Build the operator
make build

# Deploy to your cluster
make deploy
```

## Usage

### Creating a Deployment Monitor

Create a monitor for your deployment by defining a custom resource:

```yaml
apiVersion: monitor.koolhandluke.dev/v1alpha1
kind: DeploymentMonitor
metadata:
  name: my-deployment-monitor
  namespace: default
spec:
  deployment:
    name: my-app
    namespace: default
  notifications:
    slack:
      enabled: true
      channel: "#deployments"
    holmes:
      enabled: true
      endpoint: "https://holmes.example.com"
  timeout: 600s
```

Apply the resource:

```bash
kubectl apply -f deployment-monitor.yaml
```

## Architecture

This operator follows the Kubebuilder pattern and includes:

- **Controllers**: Watch Deployment resources and manage monitoring state
- **Custom Resources**: Define monitoring behavior and configuration
- **Webhooks**: Validate and mutate incoming resources
- **Integration Clients**: Holmes and Slack notification handlers

## Configuration

### Slack Integration

Set the following environment variables:

```bash
SLACK_TOKEN=xoxb-your-token
SLACK_CHANNEL=your-channel
```

### Holmes Integration

Configure Holmes endpoint and credentials:

```bash
HOLMES_ENDPOINT=https://holmes.example.com
HOLMES_API_KEY=your-api-key
```

## Development

### Prerequisites for Development

- Go 1.16+
- Kubebuilder v3+
- Docker (for building container images)

### Running Locally

```bash
# Generate API manifests and CRDs
make manifests

# Run the operator locally
make run
```

### Testing

```bash
# Run unit tests
make test

# Generate test coverage
make test-coverage
```

### Building Container Image

```bash
# Build and push container image
make docker-build docker-push IMG=your-registry/k8s-deploy-monitor-operator:latest
```

## Contributing

Contributions are welcome! Please follow these steps:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/your-feature`)
3. Commit your changes (`git commit -am 'Add your feature'`)
4. Push to the branch (`git push origin feature/your-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Support

For issues, questions, or contributions, please open an issue on the [GitHub repository](https://github.com/koolhandluke/k8s-deploy-monitor-operator/issues).

## Roadmap

- [ ] Additional notification channels
- [ ] Enhanced monitoring metrics and dashboards
- [ ] Performance optimizations for large-scale deployments
- [ ] Helm chart for simplified installation
- [ ] Comprehensive API documentation

---

**Built with ❤️ using Kubebuilder**
