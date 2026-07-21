# Contributing to Archive Email Downloader

We welcome contributions to this project! To make sure your contributions can be merged as smoothly as possible, please follow these guidelines.

## How to Contribute

### 1. Reporting Bugs
* Check the existing issues to see if the bug has already been reported.
* If not, open a new issue. Please include:
  * A clear description of the bug.
  * Steps to reproduce it.
  * Expected vs actual behavior.
  * Relevant logs or error messages (ensure you sanitize any private credentials/IPs).

### 2. Suggesting Enhancements
* Open an issue describing the feature or improvement you want to see.
* Explain the use case and why it would be beneficial to other users.

### 3. Pull Requests
* Fork the repository and create your branch from `main`.
* Ensure your code adheres to standard Go formatting guidelines (`go fmt`).
* Write unit tests for new functionality.
* Run the tests locally: `go test ./...`
* Submit a PR with a clear description of the changes.

## Development Setup

1. Clone the repository.
2. Install Go (version 1.24 or higher).
3. Build the project to verify setup:
   ```bash
   go build -o archive-downloader ./cmd/downloader
   go build -o cleanup-storage ./cmd/cleanup
   ```
4. Run tests:
   ```bash
   go test ./...
   ```

## License

By contributing to this repository, you agree that your contributions will be licensed under the project's GNU GPL v3 license.
