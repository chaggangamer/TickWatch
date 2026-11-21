# Security policy

## Reporting a vulnerability

Do not open a public issue for security problems.

Use GitHub security advisories on this repository ("Report a vulnerability" under
the Security tab). Reports go to the maintainers privately and stay private until
a fix ships.

Include what you found, how to reproduce it, the version you tested against, and
your assessment of impact. You will get an acknowledgement within a few days and
updates as the fix progresses.

## Scope

In scope: the Tickwatch server, the dashboard, the REST API, the CLI, the Docker
image, and the embedded check engine (SSRF handling, credential storage, session
handling).

Out of scope: vulnerabilities that require physical access to a host running
Tickwatch, or issues in deployment infrastructure outside the image and binary.

## Supported versions

Security fixes land on the latest release. Upgrade before reporting if you are
behind.
