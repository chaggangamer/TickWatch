# Contributing to Tickwatch

Thank you for looking at the code. This document covers the practical bits for
building, testing and submitting changes.

## Development setup

You need Go 1.24 or newer (the go.mod file states the exact minimum). No other
toolchain requirements: the project uses no CGO.

```sh
git clone https://github.com/tickwatch/tickwatch
cd tickwatch
go build ./cmd/tickwatch
./tickwatch serve
```

## Everyday commands

```sh
make build    # build bin/tickwatch
make test     # run all tests
make race     # run tests with the race detector
make vet      # go vet
make lint     # staticcheck
make cover    # coverage report
make run      # build and serve
```

The test suite creates temporary SQLite files in t.TempDir() and binds local
listeners; it needs no external services and no network access beyond localhost.

## Commit style

Short, lowercase, imperative: "add tcp checker", "fix retry counter reset". One
logical change per commit. No conventional-commit prefixes.

## Pull requests

- Keep the change focused. If it fixes a bug, include a test that fails without it.
- Run make vet, make lint and make race before pushing.
- Follow the existing code style: small functions, no stuttering names, errors
  wrapped with context, no comments restating the code.
- Keep comments to the why for non-obvious decisions only.
- New dependencies need a justification in the PR description.

## Reporting bugs

Open an issue with the version (`tickwatch version`), what you expected, what
happened, and the relevant log lines. Redact tokens and URLs that are not public.

## Security issues

Do not open public issues for security problems. Open a GitHub security advisory
instead so fixes can land before details are public.
