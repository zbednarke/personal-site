# Security remediation, October 25, 2026

This is a source/dependency review, not a statement that production is patched.
The audit session has Git transport access but no configured GCP identity or
GitHub API access. Direct live inspection is unavailable from the agent session;
the main-only inventory workflow now uses the existing deploy identity instead.
The existing GitHub deployment workflow successfully shipped commit 65fe450
(Go/dependency fixes) on October 26, run 40269226093; this does not rebuild the
VM's separate auth or Python services.
The Debian 13 runtime follow-up (9a1bb4f) also deployed successfully in run
40271452136.

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

## Container OS/media follow-up, October 26

Rebuilding the exact runtime-package recipe on Debian 12.15 and scanning it
with Trivy 0.76.1 reported 72 distinct high/critical OS advisories (273
package-level findings), including seven critical advisories. Debian 12
provided no fixed package versions for those findings. Testing the equivalent
Debian 13.7 runtime reduced that to 42 distinct advisories (208 package-level
findings), with one critical advisory remaining. Both API and repair images
now use Debian trixie. The API runtime's FFmpeg is 7.1.5-0+deb13u1 in the tested
build. These are locally rebuilt package layers, not scans of production image
digests; the Go binary is checked separately by govulncheck.

The remaining critical finding is CVE-2026-6653 in libxml2. Sixteen FFmpeg
advisories and other high-severity distribution findings also remain without
distribution fixes. This is partial remediation, not a vulnerability-free
image or proof that every flagged code path is reachable by this application.
The Debian 13 comparison also introduces two reported advisories absent from
the Debian 12 report (CVE-2026-66041 and CVE-2026-86139); overall the distinct
high/critical count falls by 30 and the critical count falls from seven to one.
Do not hide remaining findings by dropping unfixed advisories from reports.

The reusable runtime check builds the production OS/media layer, exercises
actual AAC/Opus analysis and both video/audio-only Magic Film renders with
synthetic fixtures, then publishes all high/critical OS findings as a CI
artifact. It runs daily and as part of the pre-deploy test workflow. Available
distribution fixes fail the gate; unfixed findings remain visible as warnings
and in the artifact. No paid provider calls are made by the render test.

## Production work still required

### Verified VM runtime deployments

Auth run 40274817413 verified Go 1.27.3. Python run 40276139279 verified the
film service's isolated Pillow 13.0.0 interpreter with unchanged NumPy 1.24.2;
all three services were active and private-route checks passed. Debian's old
Pillow package is retained but no longer supplies this service's Pillow.

### Proxy and reboot follow-up

The proxy inventory found Caddy 2.11.4 compiled with Go 1.26.3. The official
2.11.7 archive was verified against its SHA-512 vendor checksum, but its Go
1.26.8 build still reports standard-library/x/net vulnerabilities. The maintained
`deploy/caddy-runtime` module wraps upstream's unchanged standard command/modules
at 2.11.7, compiled with Go 1.27.3 and updated x/net/crypto/text dependencies.
Its source scan reports zero reachable or imported-package vulnerabilities.
Binary/module scanning still flags x/crypto/openpgp at module granularity;
`go list -deps` confirms that package is not linked, and CI rejects its import.
Actual HTTP and HTTP/2 over certificate-verified TLS tests pass. Test storage
is explicitly temporary and configuration persistence is disabled.

The proxy installer requires all currently installed module IDs, refuses
downgrades or concurrent changes, validates the unchanged Caddyfile as the
service user with its configured environment files, and preserves ownership,
mode and existing file capabilities. Restart/access failure restores a private
binary backup. It does not edit routes, systemd service definitions, keys or
data. This temporarily replaces the vendor-owned binary; dpkg's version can
therefore differ from `caddy version`. A recorded binary hash and daily live
inventory detect vendor overwrites for review; normal apt updates are not disabled.
Go dependency scans and weekly proposals cover the maintained proxy module.

The owner approved a shared-host reboot after the patches finish. The workflow
first verifies all three patched runtimes, then uses a root-owned one-time
approval record expiring October 27 UTC. It refuses active film jobs, and a
changed boot ID consumes the approval so reruns cannot cause a second reboot.
Post-boot checks require active services, the expected proxy hash and patched
interpreters, private-route checks and both public homepages. A successful SSH
reboot command alone does not count as a verified reboot.

The October 26 read-only VM receipt (run 40273694786) found Go 1.24.7 in the
auth binary, system Pillow 9.4.0 (Debian package 9.4.0-1.1+deb12u1), NumPy
1.24.2, FFmpeg 5.1.9 and Caddy 2.11.4 on Debian 12. The film service uses the
system Python interpreter. All three relevant services were active. A reboot
is pending; it has not been performed. Distro backports must be considered
before equating distro package versions to PyPI's vulnerability ranges.

The auth binary records source commit 4d7e6d2, outside main's ancestry. It was
recovered from this repository and its entire `jazz-api/cmd/jazz-auth` tree
is byte-identical to main. The auth security workflow verifies that comparison,
tests behavior and reachable advisories, builds with Go 1.27.3, and uses the
existing byte-verified uploader/deploy identity. Its installer accepts only
accounted-for clean source builds, validates the unchanged live configuration,
checks baseline private routes, retains a private old-binary backup, and rolls
back on restart/readiness/access-check failure. It changes no configuration,
session keys, Caddy routes, IAM bindings or buckets.
Run 40274817413 subsequently completed successfully: the installed auth binary
reports Go 1.27.3 and clean source bf35510, with all three services active.

The Python follow-up stages a root-owned venv using checksum-pinned Pillow
13.0.0 and pip wheels, entirely offline on the VM. It preserves system NumPy
1.24.2 and does not install or replace Debian packages or application code.
The exact Python 3.11 / NumPy 1.24.2 / Pillow 13.0.0 combination passed all 48
film tests in a Debian 12 container. Thirteen installer tests cover checksums,
idle guards, compatibility failures and rollback. The installer checks for
active jobs both before staging and before restart, runs compatibility and
private-route checks first, then applies only a service-specific interpreter
override. A runtime version precheck and post-restart access checks protect the
switch; failure restores the previous override and verifies the old service.
The read-only receipt distinguishes system Pillow from the service's actual
interpreter, so retaining Debian's old package does not hide which one is used.

Read-only inventory and mutating VM maintenance use separate concurrency
groups from application deployment. Auth and Python changes share a maintenance
group so they do not overlap. Inventory-only changes do not redeploy the API.

1. Complete and verify the Python service rollout. The Go/API dependencies,
   Debian 13 API runtime and separate VM auth binary are already patched.
   Check API health, Google sign-in, private pages and media after each release.
2. Plan a maintenance window for the pending host reboot; do not interrupt
   recordings, renders or unrelated services on this shared VM.
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
