"""Install a tested worker binary and minimal runtime config over IAP.

Does not start the worker, change the website provider, or copy any Codex login.
Credentials travel over SSH stdin and are never written in this checkout.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--project', default='parabolio-prod')
    parser.add_argument('--zone', default='us-central1-a')
    args = parser.parse_args()
    cli = os.environ.get('GCLOUD', 'gcloud')
    def run(*command, **kwargs):
        result = subprocess.run([cli, *command], capture_output=True, **kwargs)
        if result.returncode:
            # stderr can include failed request payloads; don't print config.
            raise SystemExit('gcloud command failed: '+ ' '.join(command[:3]))
        return result.stdout
    region = args.zone.rsplit('-', 1)[0]
    api = json.loads(run('run','services','describe','jazz-api','--project',args.project,'--region',region,'--format=json'))
    template = api['spec']['template']
    settings = {v['name']:v for v in template['spec']['containers'][0].get('env',[])}
    power = settings.get('MAGIC_FILM_VM_POWER_CONTROL', {}).get('value') == '1'
    if power:
        caller = f'jazz-film-wake@{args.project}.iam.gserviceaccount.com'
        expected = {'MAGIC_FILM_VM_PROJECT':args.project, 'MAGIC_FILM_VM_ZONE':args.zone,
                    'MAGIC_FILM_VM_NAME':'jazz-film-private', 'MAGIC_FILM_PROVIDER':'codex',
                    'MAGIC_FILM_WAKE_CALLER':caller, 'MAGIC_FILM_WAKE_AUDIENCE':api['status']['url']}
        if any(settings.get(k, {}).get('value') != v for k,v in expected.items()):
            raise SystemExit('API wake target/identity does not match this worker; refusing idle shutdown')
        try:
            with urllib.request.urlopen(api['status']['url']+'/health/film-power', timeout=15) as response:
                capability = json.load(response)
        except Exception:
            raise SystemExit('Cannot verify deployed film power capability; refusing idle shutdown')
        if capability != {'capability':'private-film-vm-power-v1', 'enabled':True}:
            raise SystemExit('Deployed API is not ready for worker idle shutdown')
        scheduler = json.loads(run('scheduler','jobs','describe','jazz-film-wake','--project',args.project,'--location',region,'--format=json'))
        target = scheduler.get('httpTarget', {})
        oidc = target.get('oidcToken', {})
        if (scheduler.get('state') != 'ENABLED' or scheduler.get('schedule') != '* * * * *'
                or target.get('uri') != api['status']['url']+'/internal/magic-film/reconcile'
                or target.get('httpMethod') != 'POST' or oidc.get('serviceAccountEmail') != caller
                or oidc.get('audience') != api['status']['url']):
            raise SystemExit('Expected enabled film wake scheduler/target before installing an idle-stopping worker')
    def setting(name):
        item = settings[name]
        if 'value' in item: return item['value']
        secret = item['valueFrom']['secretKeyRef']
        return run('secrets','versions','access',secret.get('key','latest'),'--secret',secret['name'],'--project',args.project).decode().strip()
    instance = template['metadata']['annotations']['run.googleapis.com/cloudsql-instances']
    if ',' in instance: raise SystemExit('Expected exactly one Cloud SQL instance')
    config = {
        'DATABASE_URL':setting('DATABASE_URL'), 'GCS_BUCKET':setting('GCS_BUCKET'),
        'GCP_SERVICE_ACCOUNT':f'jazz-film-private@{args.project}.iam.gserviceaccount.com',
        'MAGIC_FILM_PRIVATE_WORKER':'1', 'MAGIC_FILM_PROVIDER':'codex',
        'MAGIC_FILM_SQL_INSTANCE':instance, 'MAGIC_FILM_WORKSPACE':'/var/lib/magic-film/jobs',
        'MAGIC_FILM_VM_POWER_CONTROL':'1' if power else '0',
        'MAGIC_FILM_IDLE_SECONDS':settings.get('MAGIC_FILM_IDLE_SECONDS', {}).get('value', '300'),
    }
    env = ''.join(name+'='+json.dumps(value)+'\n' for name,value in config.items()).encode()
    ssh = ['compute','ssh','jazz-film-private','--project',args.project,'--zone',args.zone,'--tunnel-through-iap','--quiet']
    run(*ssh,'--command=sudo test -f /var/lib/magic-film/bootstrap-ready')
    digest=hashlib.sha256(args.binary.read_bytes()).hexdigest()
    run('compute','scp',str(args.binary),'jazz-film-private:private-film-jazz-api.new','--project',args.project,'--zone',args.zone,'--tunnel-through-iap','--quiet')
    helper = Path(__file__).with_name('magic-film-idle-stop.py')
    helper_digest = hashlib.sha256(helper.read_bytes()).hexdigest()
    run('compute','scp',str(helper),'jazz-film-private:magic-film-idle-stop.new','--project',args.project,'--zone',args.zone,'--tunnel-through-iap','--quiet')
    install_helper = (f'test "$(sha256sum magic-film-idle-stop.new | cut -d " " -f1)" = "{helper_digest}" && '
        'sudo install -o root -g root -m 0555 magic-film-idle-stop.new /usr/local/libexec/magic-film-idle-stop && '
        'rm magic-film-idle-stop.new')
    run(*ssh,'--command='+install_helper)
    rule = b'magic-film ALL=(root) NOPASSWD: /usr/local/libexec/magic-film-idle-stop ""\n'
    run(*ssh,'--command=sudo sh -c \'set -e; umask 077; cat > /etc/sudoers.d/magic-film-power.new; chmod 0440 /etc/sudoers.d/magic-film-power.new; visudo -cf /etc/sudoers.d/magic-film-power.new; mv /etc/sudoers.d/magic-film-power.new /etc/sudoers.d/magic-film-power\'',input=rule)
    run(*ssh,'--command=sudo sh -c \'umask 077; cat > /etc/magic-film/worker.env.new; chmod 600 /etc/magic-film/worker.env.new; mv /etc/magic-film/worker.env.new /etc/magic-film/worker.env\'',input=env)
    command = f'test "$(sha256sum private-film-jazz-api.new | cut -d " " -f1)" = "{digest}" && sudo systemctl stop magic-film-worker.service && sudo install -o root -g root -m 0755 private-film-jazz-api.new /opt/magic-film/jazz-api && rm private-film-jazz-api.new'
    run(*ssh,'--command='+command)
    print('Installed verified worker binary '+digest+'. Idle power control: '+str(power)+'. Service remains stopped until operator verification/start. No subscription credentials were transferred.')


if __name__=='__main__': main()
