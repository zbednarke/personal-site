"""Create the zero-idle Magic Film Cloud Run Job from jazz-api's image.

The job copies jazz-api's database, bucket, Cloud SQL, VPC and service-account
configuration, adds OPENAI_API_KEY from Secret Manager, and lets only that
service account invoke the job. It also configures jazz-api with the job name.

Dry run by default; add --apply after reviewing the commands:
  python3 deploy/create-magic-film-job.py [--apply]
"""
import argparse
import json
import os
import shlex
import subprocess
import sys
import tempfile

PROJECT, REGION, JOB = "parabolio-prod", "us-central1", "jazz-magic-film"


def gcloud_json(*args):
    result = subprocess.run(["gcloud", *args, "--format", "json"], check=True, capture_output=True, text=True)
    return json.loads(result.stdout or "null")


def show(command):
    return " ".join(shlex.quote(part) for part in command)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--project", default=PROJECT)
    parser.add_argument("--region", default=REGION)
    parser.add_argument("--job", default=JOB)
    parser.add_argument("--openai-secret", default="magic-film-openai-api-key")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    api = gcloud_json("run", "services", "describe", "jazz-api", "--project", args.project, "--region", args.region)
    if subprocess.run(["gcloud", "secrets", "describe", args.openai_secret, "--project", args.project], capture_output=True).returncode:
        sys.exit(f"Secret {args.openai_secret} does not exist in {args.project}")
    template = api["spec"]["template"]
    container = template["spec"]["containers"][0]
    annotations = template["metadata"].get("annotations", {})
    account = template["spec"].get("serviceAccountName", "")
    if not account:
        sys.exit("jazz-api has no explicit runtime service account")
    env, secrets = {}, []
    for item in container.get("env", []):
        name = item["name"]
        if name == "PORT" or name.startswith("MAGIC_FILM_") or name == "OPENAI_API_KEY":
            continue
        ref = (item.get("valueFrom") or {}).get("secretKeyRef")
        if ref:
            secrets.append(f"{name}={ref['name']}:{ref.get('key', 'latest')}")
        else:
            env[name] = item.get("value", "")
    secrets.append(f"OPENAI_API_KEY={args.openai_secret}:latest")
    descriptor, env_path = tempfile.mkstemp(prefix="magic-film-env-", suffix=".yaml")
    try:
        with os.fdopen(descriptor, "w") as output:
            for key, value in env.items():
                output.write(f"{key}: {json.dumps(value)}\n")
        deploy = ["gcloud", "run", "jobs", "deploy", args.job, "--image", container["image"],
                  "--project", args.project, "--region", args.region, "--quiet", "--tasks", "1",
                  "--parallelism", "1", "--max-retries", "0", "--task-timeout", "45m", "--cpu", "2",
                  "--memory", "4Gi", "--service-account", account, "--env-vars-file", env_path,
                  "--set-secrets", ",".join(secrets)]
        cloudsql = annotations.get("run.googleapis.com/cloudsql-instances")
        connector = annotations.get("run.googleapis.com/vpc-access-connector")
        if cloudsql:
            deploy += ["--set-cloudsql-instances", cloudsql]
        if connector:
            deploy += ["--vpc-connector", connector]
        commands = [
            ["gcloud", "secrets", "add-iam-policy-binding", args.openai_secret, "--project", args.project,
             "--member", f"serviceAccount:{account}", "--role", "roles/secretmanager.secretAccessor", "--quiet"],
            deploy,
            ["gcloud", "run", "jobs", "add-iam-policy-binding", args.job, "--project", args.project,
             "--region", args.region, "--member", f"serviceAccount:{account}", "--role", "roles/run.invoker", "--quiet"],
            ["gcloud", "run", "services", "update", "jazz-api", "--project", args.project, "--region", args.region,
             "--update-env-vars", f"MAGIC_FILM_PROJECT={args.project},MAGIC_FILM_REGION={args.region},MAGIC_FILM_JOB={args.job}", "--quiet"],
        ]
        print("Copied environment names (values hidden): " + ", ".join(env))
        for command in commands:
            print("$ " + show(command))
            if args.apply:
                subprocess.run(command, check=True)
    finally:
        os.unlink(env_path)
    if not args.apply:
        print("\nDry run. Re-run with --apply, then set the GitHub variable MAGIC_FILM_ENABLED=true.")


if __name__ == "__main__":
    main()
