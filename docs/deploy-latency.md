# Deployment latency audit — 20 November 2026

The roughly 30-minute user wait included failed verification attempts, repeated
checks, application deployment and additional proxy maintenance. It was not a
30-minute file upload. The workflow logs identify these costs:

| Stage | Run | Measured job duration |
| --- | --- | --- |
| First failed PR check | 44796890988 | 4m38s |
| Second failed PR check (overlapped the first) | 44797093462 | 4m16s |
| Successful PR check | 44797842161 | 7m06s |
| Same checks repeated after merge | 44799090471 | 6m47s |
| Application deployment | 44800182991 | 5m52s |

The two failed checks attempted a nonexistent Python Playwright 1.58.2 release.
The npm package has that version; the Python package uses 1.58.0. That dependency
mistake was avoidable. Dependencies now have explicit lock/pin files, and the
Python installation happens before the browser job compiles the API.

Within the successful PR check, the five serial browser suites took 200 seconds.
They now run in separate jobs, alongside unit and runtime-security checks.
Node, Python, Go and Chromium downloads use caches. All original tests and
vulnerability gates remain. The final `test` check rejects failed, cancelled,
or unexpectedly skipped dependencies.

Within deployment, the API source build consumed 270 seconds (77% of the job).
Static publication took 9 seconds, proxy verification 16 seconds, Workbench
16 seconds and the film worker 4 seconds. Backend builds now depend on a diff
against each actual serving revision, after the existing provenance guard.
Homepage-only changes therefore avoid the API build and Workbench redeploy.
Unknown baselines, changed deployment policy, forced rebuilds, or backend changes
still rebuild. A first rollout of this policy necessarily rebuilds the API.

The 6m47s post-merge check is avoidable only with evidence: a recent successful
PR run, a run-attempt-specific receipt, and identical source trees for PR head,
tested merge, and merged commit. Any missing proof runs full CI. The one-hour
limit bounds reuse of dependency/security results. Manual CI always rechecks.

## Release procedure

1. Finish dependency resolution and focused local checks before starting CI.
2. Push one coherent revision; cancel superseded PR verification automatically.
3. Merge once its required checks succeed. Let automatic production deployment
   run; do not start a duplicate manual deployment.
4. Verify the production release and requested behavior. Report application
   publication separately from independently running maintenance tasks.
5. Read step timestamps for regressions before retrying. Do not repeatedly rerun
   a deterministic failure without fixing its cause.

Initial targets, not yet measured guarantees: about 3–5 minutes for fresh
verification, under 2 minutes from a verified merge to homepage publication,
and about 8–10 minutes including a fresh backend verification and deployment.
Runner queue time, cold caches and external services can add latency. Parallel
browser jobs trade additional runner-minutes for shorter wall time.

The next substantial opportunity is building one tested OCI image during CI
and promoting its immutable digest. That could remove much of the remaining
270-second Cloud Run source-build delay; it requires separate image provenance,
registry authentication and runtime validation work. It is not implemented here.
