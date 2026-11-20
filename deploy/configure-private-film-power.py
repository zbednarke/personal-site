"""Configure on-demand film VM wake-up. Read-only plan unless --apply is set.

Run after deploying the power-capable API, before installing the new worker.
No disk, VM, login or provider is deleted/replaced. No secrets are read.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project', default='parabolio-prod')
    parser.add_argument('--zone', default='us-central1-a')
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    cli = os.environ.get('GCLOUD', 'gcloud')
    project, zone, vm = args.project, args.zone, 'jazz-film-private'
    region = zone.rsplit('-', 1)[0]

    def read(parts, optional=False):
        result = subprocess.run([cli, *parts, '--format=json'], capture_output=True, text=True)
        if result.returncode:
            if optional and ('NOT_FOUND' in result.stderr or 'was not found' in result.stderr):
                return None
            raise SystemExit('Cannot inspect '+shlex.join(parts[:3])+'; check identity/permissions')
        return json.loads(result.stdout)

    api = read(['run','services','describe','jazz-api','--project',project,'--region',region])
    url = api['status']['url'].rstrip('/')
    account = api['spec']['template']['spec'].get('serviceAccountName')
    if not account or not account.endswith('.gserviceaccount.com'):
        raise SystemExit('API runtime service account must be explicit')
    settings = {v['name']:v.get('value') for v in api['spec']['template']['spec']['containers'][0].get('env', [])}
    if settings.get('MAGIC_FILM_PROVIDER') != 'codex':
        raise SystemExit('Deploy the subscription worker provider before configuring its lifecycle')
    try:
        with urllib.request.urlopen(url+'/health/film-power', timeout=15) as response:
            capability = json.load(response)
    except Exception:
        raise SystemExit('Deploy the power-capable API first; capability endpoint unavailable')
    if capability.get('capability') != 'private-film-vm-power-v1':
        raise SystemExit('Deploy the power-capable API before configuring lifecycle')
    role_id = 'jazzFilmVmStarter'
    role = read(['iam','roles','describe',role_id,'--project',project], optional=True)
    permissions = ['compute.instances.get','compute.instances.start']
    if role and (sorted(role.get('includedPermissions', [])) != permissions or role.get('deleted')):
        raise SystemExit('Existing custom starter role differs; refusing to broaden/reuse it')
    caller = f'jazz-film-wake@{project}.iam.gserviceaccount.com'
    sa = read(['iam','service-accounts','describe',caller,'--project',project], optional=True)
    job_base = ['scheduler','jobs']
    job_target = ['jazz-film-wake','--project',project,'--location',region]
    job = read([*job_base,'describe',*job_target], optional=True)
    commands = []
    if not role:
        commands.append(['iam','roles','create',role_id,'--project',project,'--title=Start only the private film VM',
                         '--permissions='+','.join(permissions),'--stage=GA'])
    commands.append(['compute','instances','add-iam-policy-binding',vm,'--project',project,'--zone',zone,
                     '--member=serviceAccount:'+account,'--role=projects/'+project+'/roles/'+role_id])
    if not sa:
        commands.append(['iam','service-accounts','create','jazz-film-wake','--project',project,'--display-name=Private film wake reconciler'])
    commands.append(['run','services','add-iam-policy-binding','jazz-api','--project',project,'--region',region,
                     '--member=serviceAccount:'+caller,'--role=roles/run.invoker'])
    # Pause an existing job before replacing its target. Cloud Scheduler creates
    # new jobs enabled; the API flag stays off until after creation below.
    if job and job.get('state') == 'ENABLED':
        commands.append([*job_base,'pause',*job_target])
    commands.append([*job_base,'update' if job else 'create','http',*job_target,
        '--schedule=* * * * *','--time-zone=Etc/UTC','--uri='+url+'/internal/magic-film/reconcile',
        '--http-method=POST','--oidc-service-account-email='+caller,'--oidc-token-audience='+url,
        '--attempt-deadline=30s','--max-retry-attempts=3','--min-backoff=10s','--max-backoff=60s',
        '--max-doublings=2','--max-retry-duration=120s'])
    commands.append(['compute','instances','add-metadata',vm,'--project',project,'--zone',zone,
        '--metadata-from-file=startup-script='+str(Path(__file__).with_name('private-film-worker-init.sh').resolve())])
    settings = {'MAGIC_FILM_VM_POWER_CONTROL':'1','MAGIC_FILM_IDLE_SECONDS':'300',
                'MAGIC_FILM_VM_PROJECT':project,'MAGIC_FILM_VM_ZONE':zone,'MAGIC_FILM_VM_NAME':vm,
                'MAGIC_FILM_WAKE_CALLER':caller,'MAGIC_FILM_WAKE_AUDIENCE':url}
    commands.append(['run','services','update','jazz-api','--project',project,'--region',region,
                     '--update-env-vars='+','.join(k+'='+v for k,v in settings.items())])
    if job:
        commands.append([*job_base,'resume',*job_target])
    for command in commands:
        print(shlex.join([cli, *command]), flush=True)
        if args.apply:
            result = subprocess.run([cli, *command, '--quiet'], capture_output=True, text=True)
            if result.returncode:
                raise SystemExit('Configuration stopped at '+shlex.join(command[:3])+'. Worker idle shutdown has not been installed; inspect GCP before retrying.')
    print('Wake infrastructure configured. Install/start the new worker next; verify wake and idle poweroff live.' if args.apply else 'Read-only plan. Add --apply to configure wake infrastructure before installing the worker.')


if __name__ == '__main__':
    main()
