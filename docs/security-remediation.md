# Security remediation, October 25, 2026

This is a source/dependency review, not a statement that production is patched.
The audit session has Git transport access but no configured GCP identity or
GitHub API access. No live VM, Cloud Run, database or bucket settings were read
or changed.

## Changes ready for review

- Replace the unsupported Go 1.24 build images and CI toolchain with Go 1.27.3.
- Update the vulnerable Go dependency families (HTTP/2, image decoding, text,
  PostgreSQL driver, gRPC and OpenTelemetry) and their required dependencies.
- Refresh the Jazz JavaScript test dependency lockfile.
- Gate the existing test-to-deploy workflow on `govulncheck` and npm audit, and
  add daily scans and weekly dependency update proposals.

The baseline Go symbol scan reported 17 vulnerabilities from six modules. The
updated symbol scan reports no reachable vulnerabilities. Module-only scanning
still flags the unmaintained x/crypto/openpgp package; this app does not import
it (x/crypto is required for bcrypt and other maintained packages). A clean
scan does not establish that a particular cloud deployment is current.

The official Go advisory data was obtained from `golang/vulndb`'s OSV files on
GitHub because this environment cannot reach vuln.go.dev. Verification uses
the same govulncheck analyzer with that local database.

## Python media dependency follow-up, October 26

The previous `Pillow>=9.4,<13` requirement resolved to Pillow 12.3.0. The
current PyPI advisory service reports 37 vulnerabilities in that version, all
fixed in 13.0.0, including CVE-2026-112398 and CVE-2026-114501. The requirement
now pins Pillow 13.0.0, which requires Python 3.11 or newer. NumPy's existing
range does not need to change for this remediation. Daily scans and the
pre-deployment test workflow now audit the Python requirements with pip-audit;
Dependabot also covers this requirements file.

This is a vulnerable-dependency finding, not evidence that all affected image
formats or operations are reachable through this application's endpoints.
The service primarily uses Pillow to generate graphics. The production VM
uses a separately installed Python interpreter and packages; its installed
versions remain unverified and are not updated by the static/API deploy.

## Production work still required

1. Merge and deploy the reviewed dependency patch through the normal guarded
   workflow. Check API health, Google sign-in, private pages and media after it.
2. Rebuild and install the VM's separate `jazz-auth` binary with the supported
   Go compiler. Updating the Cloud Run image does not replace that binary.
3. Inventory and patch the site VM's OS, Caddy, Python, Pillow, NumPy and FFmpeg.
   The current repository includes a hosted film service on that VM; Cloud Run
   image updates do not patch its dependencies. Verify services after updates;
   schedule any required reboot rather than restarting active recordings.
4. Inspect GCP firewall rules, service-account bindings, Cloud SQL exposure and
   bucket policies using an authorized GCP identity. `setup-deploy-auth.sh`
   grants broad project deployment roles; do not describe these as an audited
   least-privilege production policy without inspecting the actual bindings.
5. Confirm exposed development/session credentials have been revoked through
   their providers. Never put replacement credential values in reports or Git.

Neither this Go/static site nor the inspected Ugga marketing export uses PHP.
PHP2Shell is therefore not a source-level match for these checked-in apps;
the VM inventory still needs to establish whether other hosted software uses it.
