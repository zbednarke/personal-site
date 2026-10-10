"""Create (or update) the Workbench Cloud Run service from the API's image. Run once, by hand.

Workbench is the jazz-api image started with WORKBENCH_MODE=1 as its own
service, so it can hold event streams open (concurrency 40, not 1) and keep
its CPU between requests (the agent loop outlives the request that started
it). It copies jazz-api's runtime service account, Cloud SQL instances and
environment (plain values and Secret Manager references alike; values are
never printed), then adds the Workbench settings:

  WORKBENCH_MODE=1
  WORKBENCH_OWNER_SUBJECT       the X-Jazz-User value of the owner (jazz-auth's User)
  ANTHROPIC_API_KEY             secret prism-anthropic-api-key
  WORKBENCH_GITHUB_TOKEN        secret workbench-github-token (optional; without it GitHub writes are refused)
  WORKBENCH_VAPID_PRIVATE_KEY   secret workbench-vapid-private-key (optional; without it there is no Web Push)
  WORKBENCH_VAPID_PUBLIC_KEY    the matching public key
  WORKBENCH_MONTHLY_CAP_USD     the monthly spend cap (default 25)

Dry run by default: it prints the gcloud commands. Add --apply to run them.

  python3 deploy/create-workbench-service.py --owner <user> --vapid-public <key> [--apply]
"""
import argparse, json, os, shlex, subprocess, sys, tempfile

PROJECT, REGION = 'parabolio-prod', 'us-central1'
SKIP_ENV = {'PORT'}


def gcloud_json(*args):
    out = subprocess.run(['gcloud', *args, '--format', 'json'], check=True, capture_output=True, text=True).stdout
    return json.loads(out or 'null')


def secret_exists(name, project):
    return subprocess.run(['gcloud', 'secrets', 'describe', name, '--project', project], capture_output=True).returncode == 0


def build_commands(api, iam, args, secrets_present, env_file='ENV_FILE'):
    """Pure: the environment to write to env_file and the gcloud commands, from jazz-api's description."""
    template = api['spec']['template']
    container = template['spec']['containers'][0]
    annotations = template['metadata'].get('annotations', {})
    account = template['spec'].get('serviceAccountName', '')
    env, secret_env = {}, []
    for e in container.get('env', []):
        name = e['name']
        if name in SKIP_ENV or name.startswith('WORKBENCH_') or name == 'ANTHROPIC_API_KEY':
            continue
        ref = (e.get('valueFrom') or {}).get('secretKeyRef')
        if ref:
            secret_env.append(f"{name}={ref['name']}:{ref.get('key', 'latest')}")
        else:
            env[name] = e.get('value', '')
    env.update({'WORKBENCH_MODE': '1', 'WORKBENCH_OWNER_SUBJECT': args.owner, 'WORKBENCH_MONTHLY_CAP_USD': str(args.cap)})
    secret_env.append(f'ANTHROPIC_API_KEY={args.anthropic_secret}:latest')
    new_secrets = [args.anthropic_secret]
    if secrets_present.get(args.github_secret):
        secret_env.append(f'WORKBENCH_GITHUB_TOKEN={args.github_secret}:latest')
        new_secrets.append(args.github_secret)
    if secrets_present.get(args.vapid_secret) and args.vapid_public:
        secret_env.append(f'WORKBENCH_VAPID_PRIVATE_KEY={args.vapid_secret}:latest')
        env['WORKBENCH_VAPID_PUBLIC_KEY'] = args.vapid_public
        new_secrets.append(args.vapid_secret)
    commands = []
    if account:
        for s in new_secrets:
            commands.append(['gcloud', 'secrets', 'add-iam-policy-binding', s, '--project', args.project,
                             '--member', f'serviceAccount:{account}', '--role', 'roles/secretmanager.secretAccessor', '--quiet'])
    deploy = ['gcloud', 'run', 'deploy', 'workbench', '--image', container['image'], '--project', args.project, '--region', args.region, '--quiet',
              '--timeout', '3600', '--concurrency', '40', '--cpu', '1', '--memory', '512Mi', '--no-cpu-throttling',
              '--min-instances', '0', '--max-instances', '2', '--env-vars-file', env_file, '--set-secrets', ','.join(secret_env)]
    if account:
        deploy += ['--service-account', account]
    if annotations.get('run.googleapis.com/cloudsql-instances'):
        deploy += ['--set-cloudsql-instances', annotations['run.googleapis.com/cloudsql-instances']]
    if annotations.get('run.googleapis.com/vpc-access-connector'):
        deploy += ['--vpc-connector', annotations['run.googleapis.com/vpc-access-connector']]
    public = any('allUsers' in b.get('members', []) and b.get('role') == 'roles/run.invoker' for b in (iam or {}).get('bindings', []))
    deploy.append('--allow-unauthenticated' if public else '--no-allow-unauthenticated')
    commands.append(deploy)
    return env, commands


def show(cmd):
    return ' '.join(shlex.quote(p) for p in cmd)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--owner', required=True, help="the owner's X-Jazz-User value (jazz-auth's User)")
    p.add_argument('--vapid-public', default='', help='WORKBENCH_VAPID_PUBLIC_KEY (from go run ./cmd/workbench-vapid)')
    p.add_argument('--cap', default='25', help='monthly spend cap in USD')
    p.add_argument('--anthropic-secret', default='prism-anthropic-api-key')
    p.add_argument('--github-secret', default='workbench-github-token')
    p.add_argument('--vapid-secret', default='workbench-vapid-private-key')
    p.add_argument('--project', default=PROJECT)
    p.add_argument('--region', default=REGION)
    p.add_argument('--apply', action='store_true', help='run the commands (default: print them)')
    args = p.parse_args()
    api = gcloud_json('run', 'services', 'describe', 'jazz-api', '--project', args.project, '--region', args.region)
    iam = gcloud_json('run', 'services', 'get-iam-policy', 'jazz-api', '--project', args.project, '--region', args.region)
    present = {s: secret_exists(s, args.project) for s in (args.anthropic_secret, args.github_secret, args.vapid_secret)}
    if not present[args.anthropic_secret]:
        sys.exit(f'Secret {args.anthropic_secret} not found in {args.project}')
    for s in (args.github_secret, args.vapid_secret):
        if not present[s]:
            print(f'Note: secret {s} not found; continuing without it.')
    fd, env_file = tempfile.mkstemp(prefix='workbench-env-', suffix='.yaml')  # mode 0600
    try:
        env, commands = build_commands(api, iam, args, present, env_file)
        with os.fdopen(fd, 'w') as f:
            for k, v in env.items():
                f.write(f'{k}: {json.dumps(v)}\n')  # JSON strings are valid YAML scalars
        print('Environment (values not shown): ' + ', '.join(env))
        for cmd in commands:
            print('$ ' + show(cmd))
            if args.apply:
                subprocess.run(cmd, check=True)
    finally:
        os.unlink(env_file)
    if not args.apply:
        print('\nDry run. Re-run with --apply to create or update the service.')
        return
    url = gcloud_json('run', 'services', 'describe', 'workbench', '--project', args.project, '--region', args.region)['status']['url']
    print(f'\nWorkbench is at {url}. Next: add WORKBENCH_API_URL={url} to /etc/caddy/jazz.env on the site VM,')
    print('run deploy/install-workbench-route.py there, and set the repository variable WORKBENCH_ENABLED=true.')


if __name__ == '__main__':
    main()
