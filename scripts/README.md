# scripts

Helper scripts for repository maintenance. None of them contain credentials.

## screenshots.js

Captures the four README screenshots into `docs/screenshots` from a running,
isolated Tickwatch instance with seeded demo data. Run it with the Playwright
Docker image so no browsers or Node modules are installed locally:

```sh
docker run --rm \
  -v "$(pwd -W):/work" -w /work \
  -e SHOTS_BASE=http://host.docker.internal:8081 \
  -e SHOTS_ADMIN_PASSWORD=... \
  mcr.microsoft.com/playwright:v1.49.1-jammy \
  node scripts/screenshots.js
```

`SHOTS_BASE` points at the instance, `SHOTS_ADMIN_PASSWORD` is the admin
password for that instance. The script logs in as `admin`, captures the
dashboard, the monitor detail page of monitor 1, the status page and the
dashboard in dark mode, at 1280x800 with a 2x device scale factor.

See docs/screenshots/README.md for what each capture should contain.
