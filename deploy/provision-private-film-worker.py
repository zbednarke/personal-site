"""Provision isolated, IAP-only film infrastructure; never copies Codex auth.

Creates a dedicated VM/network/service account, grants Cloud SQL connector and
recording-bucket access, and installs the pinned official CLI. Existing website
VMs and CI credentials are untouched. --apply is required for changes.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project', default='parabolio-prod')
    parser.add_argument('--zone', default='us-central1-a')
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    project, zone, name = args.project, args.zone, 'jazz-film-private'
    region = zone.rsplit('-', 1)[0]
    account = f'{name}@{project}.iam.gserviceaccount.com'
    cli = os.environ.get('GCLOUD', 'gcloud')
    def read(*parts):
        result = subprocess.run([cli, *parts, '--format=json'], capture_output=True, text=True)
        return json.loads(result.stdout) if result.returncode == 0 else None
    api = read('run', 'services', 'describe', 'jazz-api', '--project', project, '--region', region)
    if not api:
        raise SystemExit('Cannot read Jazz API configuration')
    settings = {v['name']: v for v in api['spec']['template']['spec']['containers'][0].get('env', [])}
    bucket = settings.get('GCS_BUCKET', {}).get('value')
    if not bucket:
        raise SystemExit('Expected a plain GCS_BUCKET configuration value')
    commands = []
    if not read('iam', 'service-accounts', 'describe', account, '--project', project):
        commands.append(['iam', 'service-accounts', 'create', name, '--project', project, '--display-name=Private Codex film worker'])
    if not read('compute', 'networks', 'describe', name, '--project', project):
        commands.append(['compute', 'networks', 'create', name, '--project', project, '--subnet-mode=custom'])
    if not read('compute', 'networks', 'subnets', 'describe', name, '--project', project, '--region', region):
        commands.append(['compute', 'networks', 'subnets', 'create', name, '--project', project, '--region', region, '--network', name, '--range=10.48.0.0/24', '--enable-private-ip-google-access'])
    if not read('compute', 'firewall-rules', 'describe', name+'-iap', '--project', project):
        commands.append(['compute', 'firewall-rules', 'create', name+'-iap', '--project', project, '--network', name, '--allow=tcp:22', '--source-ranges=35.235.240.0/20', '--target-tags='+name])
    commands.extend([
        ['projects', 'add-iam-policy-binding', project, '--member=serviceAccount:'+account, '--role=roles/cloudsql.client', '--condition=None'],
        ['projects', 'add-iam-policy-binding', project, '--member=serviceAccount:'+account, '--role=roles/logging.logWriter', '--condition=None'],
        ['storage', 'buckets', 'add-iam-policy-binding', 'gs://'+bucket, '--member=serviceAccount:'+account, '--role=roles/storage.objectUser'],
        ['iam', 'service-accounts', 'add-iam-policy-binding', account, '--project', project, '--member=serviceAccount:'+account, '--role=roles/iam.serviceAccountTokenCreator'],
    ])
    if not read('compute', 'instances', 'describe', name, '--project', project, '--zone', zone):
        commands.append(['compute', 'instances', 'create', name, '--project', project, '--zone', zone,
            '--machine-type=e2-medium', '--image-family=debian-13', '--image-project=debian-cloud',
            '--boot-disk-size=40GB', '--boot-disk-type=pd-balanced', '--subnet='+name,
            '--tags='+name, '--service-account='+account, '--scopes=cloud-platform', '--shielded-secure-boot',
            '--metadata-from-file=startup-script='+str(Path(__file__).with_name('private-film-worker-init.sh'))])
    for command in commands:
        print(' '.join(command), flush=True)
        if args.apply:
            result = subprocess.run([cli, *command, '--quiet'], capture_output=True, text=True)
            if result.returncode:
                raise SystemExit(result.stderr)
    print('Infrastructure ready.' if args.apply else 'Dry run; add --apply to provision. Runtime configuration, binary and login are installed separately.')


if __name__ == '__main__':
    main()
