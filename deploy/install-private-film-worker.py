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
    }
    env = ''.join(name+'='+json.dumps(value)+'\n' for name,value in config.items()).encode()
    ssh = ['compute','ssh','jazz-film-private','--project',args.project,'--zone',args.zone,'--tunnel-through-iap','--quiet']
    run(*ssh,'--command=sudo test -f /var/lib/magic-film/bootstrap-ready')
    digest=hashlib.sha256(args.binary.read_bytes()).hexdigest()
    run('compute','scp',str(args.binary),'jazz-film-private:private-film-jazz-api.new','--project',args.project,'--zone',args.zone,'--tunnel-through-iap','--quiet')
    run(*ssh,'--command=sudo sh -c \'umask 077; cat > /etc/magic-film/worker.env.new; chmod 600 /etc/magic-film/worker.env.new; mv /etc/magic-film/worker.env.new /etc/magic-film/worker.env\'',input=env)
    command = f'test "$(sha256sum private-film-jazz-api.new | cut -d " " -f1)" = "{digest}" && sudo systemctl stop magic-film-worker.service && sudo install -o root -g root -m 0755 private-film-jazz-api.new /opt/magic-film/jazz-api && rm private-film-jazz-api.new'
    run(*ssh,'--command='+command)
    print('Installed verified worker binary '+digest+'. Runtime config contains only DB, bucket and worker settings. Service remains stopped until migration/deployment and sign-in verification.')


if __name__=='__main__': main()
