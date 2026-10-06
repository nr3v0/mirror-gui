Contributing to `mirror-gui`
====

- [Contributing to `mirror-gui`](#contributing-to-mirror-gui)
  - [What should I know before I get started?](#what-should-i-know-before-i-get-started)
  - [How can I contribute?](#how-can-i-contribute)
    - [Reporting Bugs](#reporting-bugs)
    - [Requesting Enhancements](#requesting-enhancements)
    - [Your First Code Contribution](#your-first-code-contribution)
      - [Getting Started](#getting-started)
    - [Pull Requests](#pull-requests)
    - [Docs Contributions](#docs-contributions)
  - [Testing](#testing)

Welcome to our contributing guide! We are eager to receive contributions of all types.

## What should I know before I get started?

Mirror-GUI is a web-based interface for oc-mirror v2 that helps users manage OpenShift Container Platform mirroring operations. It has a React/TypeScript frontend (PatternFly 6) and a Go backend.

Before contributing, familiarize yourself with:
- The [README](README.md) for project overview and setup
- The [ARCHITECTURE](ARCHITECTURE.md) for how the application is structured
- The [API documentation](API.md) for the REST API surface
- The [TESTS](TESTS.md) documentation for the test suite

## How can I contribute?

### Reporting Bugs

Please submit bug reports as GitHub Issues. When submitting bug reports, please include:
1. A concise title
2. Steps to reproduce the issue
3. Browser and environment details (container runtime, architecture)
4. Screenshots if the issue is visual
5. Relevant server logs (available via `./local-build.sh --logs`)

### Requesting Enhancements

Please submit enhancement requests as GitHub Issues. When requesting enhancements, please include:

1. A concise title
2. A concise description of the modification
3. The conditions under which the modification would be relevant
4. The desired outcome of the modification
5. Provide step-by-step instructions of the enhancement
6. Explain the difference between enhancement and current functionality
7. Explain enhancement use cases

### Your First Code Contribution

#### Getting Started

1. Fork the repository and clone it locally
2. Install Go (version in `go.mod`) and Node.js 22, then install frontend dependencies: `npm ci`
3. Start the backend: `npm run dev:server` (runs `go run ./cmd/mirror-gui` on port 3001)
4. In a second terminal, start the frontend: `npm run dev` (Vite with hot reload on port 3000, proxying `/api` to the backend)
5. Run tests to verify your setup: `go test ./...` and `npm test`

### Pull Requests

When submitting pull requests, please ensure the following:
1. Include Go tests (`internal/server/*_test.go`) for backend changes
2. Include E2E tests for new UI features or workflows
3. Run `gofmt`, `go vet ./...` and `npm run lint` and fix any issues
4. Run `go test ./...` and `npm test` to ensure all tests pass
5. Update documentation (API.md, TESTS.md, etc.) if applicable
6. Follow existing code patterns and PatternFly conventions for UI components

### Docs Contributions

We will always need better docs! You can contribute to our documentation in the following ways:

1. Improved setup and usage guides
2. Enhanced API documentation in [API.md](API.md)
3. Tutorials for specific mirroring scenarios
4. Architecture and design documentation

## Testing

Mirror-GUI has a comprehensive test suite covering unit, integration, and end-to-end tests. See [TESTS.md](TESTS.md) for full details.

To run tests locally:

```bash
go test ./...         # backend unit and API tests
npm test              # script tests (Vitest)
npm run test:e2e      # end-to-end tests (Playwright, headless Chromium)
go vet ./...          # backend static checks
npm run lint          # frontend ESLint
```

CI runs automatically on pull requests via GitHub Actions with five parallel jobs: Go backend tests, frontend build/lint, E2E tests, shellcheck, and container image build validation.
