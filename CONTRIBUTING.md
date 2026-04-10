# Contributing to Kopia Operator

Thank you for your interest in contributing to the Kopia Operator! This document provides guidelines and instructions for contributing.

## Code of Conduct

Please be respectful and constructive in all interactions. We're building an inclusive community.

## Getting Started

### Prerequisites

- Go 1.24+
- Docker
- kubectl
- Access to a Kubernetes cluster (kind, minikube, or remote)
- [Kubebuilder](https://kubebuilder.io/) (for development)

### Setting Up Your Development Environment

1. **Fork and clone the repository**

   ```bash
   git clone https://github.com/<your-username>/kopia-operator.git
   cd kopia-operator
   ```

2. **Install dependencies**

   ```bash
   go mod download
   ```

3. **Install CRDs to your cluster**

   ```bash
   make install
   ```

4. **Run the operator locally**

   ```bash
   make run
   ```

### Running Tests

```bash
# Run unit tests
make test

# Run unit tests without webhooks (useful for local development without cert-manager)
ENABLE_WEBHOOKS=false make test

# Run end-to-end tests (requires a running cluster)
make test-e2e
```

## How to Contribute

### Reporting Issues

- Check if the issue already exists
- Use the issue template if available
- Include relevant details: Kubernetes version, operator version, logs, and steps to reproduce

### Submitting Pull Requests

1. **Create a feature branch**

   ```bash
   git checkout -b feature/your-feature-name
   ```

2. **Make your changes**

   - Follow the existing code style
   - Add tests for new functionality
   - Update documentation as needed

3. **Run checks before submitting**

   ```bash
   make lint      # Run linters
   make test      # Run tests
   make build     # Ensure it builds
   ```

4. **Commit your changes**

   - Use clear, descriptive commit messages
   - Reference issues when applicable (e.g., "Fixes #123")

5. **Push and create a Pull Request**
   - Describe what your PR does and why
   - Link related issues
   - Be responsive to review feedback

### Code Style

- Follow standard Go conventions and idioms
- Use `gofmt` for formatting
- Run `make lint` to check for issues
- Keep functions focused and reasonably sized
- Add comments for exported functions and complex logic

## Project Structure

```text
├── api/                    # CRD type definitions
│   └── backup/v1alpha1/    # API version
├── cmd/                    # Main entry point
├── config/                 # Kubernetes manifests
│   ├── crd/               # CRD definitions
│   ├── rbac/              # RBAC configuration
│   ├── manager/           # Operator deployment
│   └── samples/           # Example resources
├── docs/                   # Documentation
├── internal/controller/    # Controller implementations
└── test/                   # Test files
```

## Development Workflow

1. **Adding a new feature**

   - Update CRD types in `api/backup/v1alpha1/`
   - Run `make generate` to update generated code
   - Run `make manifests` to update CRD manifests
   - Implement controller logic in `internal/controller/backup/`

2. **Updating CRDs**

   ```bash
   make generate    # Generate DeepCopy methods
   make manifests   # Generate CRD YAML
   ```

3. **Building the operator image**

   ```bash
   make docker-build IMG=<your-registry>/kopia-operator:tag
   make docker-push IMG=<your-registry>/kopia-operator:tag
   ```

## Questions?

Feel free to open an issue for questions or discussions about potential contributions.

Thank you for contributing!
