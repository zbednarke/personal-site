"""Read-only inventory of the site's known VM runtimes; never reads app secrets.

Run on actual-server with the existing deploy identity: python3 - < this file.
Does not install packages, restart services, enumerate environment variables,
read application configuration, or print binary contents.
"""
import json
import hashlib
from pathlib import Path
import platform
import re
import subprocess
import sys
import datetime
import ipaddress


def output(args):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=15)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return result.stdout.strip() if result.returncode == 0 else None


def go_build_info(data):
    """Read only version/revision from modern Go's inline build-info header."""
    start = data.find(b'\xff Go buildinf:')
    if start < 0 or len(data) < start + 32 or not data[start + 15] & 2:
        return {'status': 'unavailable'}

    def string_at(pos):
        length = 0
        for shift in range(0, 64, 7):
            if pos >= len(data):
                raise ValueError('truncated build info')
            value = data[pos]
            pos += 1
            length |= (value & 127) << shift
            if value < 128:
                if length > 1024 * 1024 or pos + length > len(data):
                    raise ValueError('invalid build info length')
                return data[pos:pos + length].decode('utf-8', errors='replace'), pos + length
        raise ValueError('invalid build info length')

    try:
        version, pos = string_at(start + 32)
        modules, _ = string_at(pos)
    except ValueError:
        return {'status': 'unavailable'}
    if not re.fullmatch(r'go[0-9A-Za-z.+ _:-]{1,100}', version):
        return {'status': 'unavailable'}
    revision = re.search(r'\bvcs\.revision=([0-9a-f]{40})\b', modules)
    return {'status': 'read', 'go_version': version,
            'source_commit': revision.group(1) if revision else None,
            'source_modified': 'vcs.modified=true' in modules}


def auth_build_info(path=Path('/usr/local/bin/jazz-auth')):
    try:
        if path.stat().st_size > 64 * 1024 * 1024:
            return {'status': 'unavailable'}
        return go_build_info(path.read_bytes())
    except OSError:
        return {'status': 'unavailable'}


def inventory():
    release = {}
    try:
        for line in Path('/etc/os-release').read_text().splitlines():
            key, _, value = line.partition('=')
            if key in ('ID', 'VERSION_ID', 'PRETTY_NAME'):
                release[key] = value.strip('"')
    except OSError:
        pass
    packages = {}
    for name in ('caddy', 'ffmpeg', 'python3', 'python3-pil', 'python3-numpy',
                 'libc6', 'libssl3', 'libssl3t64', 'linux-image-cloud-amd64', 'linux-image-amd64', 'linux-image-generic'):
        value = output(['dpkg-query', '-W', '-f=${Version}', name])
        if value is not None:
            packages[name] = value
    python_modules = {}
    proxy_integrity = None
    try:
        expected = Path('/var/lib/site-security-maintenance/caddy-installed.sha256').read_text().strip()
        proxy_integrity = {'managed_record_present': True, 'matches_managed_build': bool(re.fullmatch(r'[0-9a-f]{64}', expected)) and hashlib.sha256(Path('/usr/bin/caddy').read_bytes()).hexdigest() == expected}
    except OSError:
        pass
    for name in ('PIL', 'numpy'):
        python_modules[name] = output(['/usr/bin/python3', '-I', '-c',
                                       'import ' + name + '; print(' + name + '.__version__)'])
    start = output(['systemctl', 'show', 'jazz-highlights', '--property=ExecStart', '--value'])
    runtime = None
    match = re.search(r'\bpath=([^ ;}]+)', start or '')
    if match and (match[1] == '/usr/bin/python3' or re.fullmatch(r'/opt/jazz-highlights-venvs/security-[0-9a-f]{40}-[A-Za-z0-9_]+/bin/python', match[1])):
        runtime = {'interpreter': match[1], 'pillow': None, 'numpy': None}
        for key, module in [('pillow', 'PIL'), ('numpy', 'numpy')]:
            runtime[key] = output([match[1], '-I', '-c', 'import ' + module + '; print(' + module + '.__version__)'])
    return {
        'os': release, 'kernel': platform.release(), 'packages': packages,
        'boot_id': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
        'system_python_modules': python_modules,
        'highlights_runtime': runtime,
        'highlights_uses_system_python': bool(start and re.search(r'\bpath=/usr/bin/python3(?:[ ;}])', start)),
        'jazz_auth': auth_build_info(),
        'caddy_build': auth_build_info(Path('/usr/bin/caddy')),
        'caddy_version': output(['/usr/bin/caddy', 'version']),
        'caddy_integrity': proxy_integrity,
        'services_active': {name: output(['systemctl', 'is-active', name]) == 'active'
                            for name in ('caddy', 'jazz-auth', 'jazz-highlights')},
        'reboot_required': Path('/var/run/reboot-required').exists(),
    }


def alias(value):
    """Keep infrastructure identities correlatable without publishing them."""
    return hashlib.sha256(str(value).encode()).hexdigest()[:12]


def iam_summary(policy):
    bindings = policy.get('bindings', [])
    return {'bindings': [{'role': b.get('role'),
                         'public': sorted(m for m in b.get('members', []) if m in ('allUsers', 'allAuthenticatedUsers')),
                         'principals': sorted(alias(m) for m in b.get('members', []) if m not in ('allUsers', 'allAuthenticatedUsers')),
                         'conditional': bool(b.get('condition'))} for b in bindings]}


def command_json(parts, fields):
    """Only read metadata. Never expose raw output/errors or SDK credentials."""
    allowed = {('projects', 'list'), ('projects', 'get-iam-policy'),
               ('compute', 'instances', 'list'), ('compute', 'firewall-rules', 'list'),
               ('run', 'services', 'list'), ('run', 'services', 'get-iam-policy'),
               ('run', 'jobs', 'list'), ('sql', 'instances', 'list'),
               ('storage', 'buckets', 'list'), ('storage', 'buckets', 'get-iam-policy'),
               ('functions', 'list'), ('functions', 'get-iam-policy'),
               ('iam', 'workload-identity-pools', 'providers', 'describe'),
               ('iam', 'service-accounts', 'keys', 'list')}
    if not any(tuple(parts[:len(p)]) == p for p in allowed):
        raise ValueError('Not a read-only inventory command')
    try:
        result = subprocess.run(['gcloud', *parts, '--quiet', '--format=json(' + fields + ')'],
                                capture_output=True, text=True, timeout=45)
    except (OSError, subprocess.TimeoutExpired) as e:
        return {'status': 'timeout' if isinstance(e, subprocess.TimeoutExpired) else 'tool_unavailable'}, None
    if result.returncode:
        message = result.stderr.lower()
        status = 'permission_denied' if any(s in message for s in ('permission', 'forbidden', '403')) else 'command_failed'
        if any(s in message for s in ('has not been used', 'is disabled', 'service_disabled')):
            status = 'api_disabled'
        return {'status': status}, None
    try:
        return {'status': 'observed'}, json.loads(result.stdout)
    except ValueError:
        return {'status': 'invalid_response'}, None


def public_source(raw):
    try:
        net = ipaddress.ip_network(raw, strict=False)
        return not (net.is_private or net.is_loopback or net.is_link_local)
    except ValueError:
        return None


def firewall_summary(rule):
    sources = rule.get('sourceRanges', [])
    return {'id': alias(rule.get('name')), 'direction': rule.get('direction'),
            'disabled': rule.get('disabled', False), 'priority': rule.get('priority'),
            'allows': rule.get('allowed', []), 'denies': rule.get('denied', []),
            'source_ranges': sources, 'internet_wide': any(s in ('0.0.0.0/0', '::/0') for s in sources),
            'public_source': any(public_source(s) for s in sources),
            'target_tags': [alias(s) for s in rule.get('targetTags', [])],
            'target_accounts': [alias(s) for s in rule.get('targetServiceAccounts', [])],
            'network': alias(rule.get('network')), 'other_source_selector': bool(rule.get('sourceTags') or rule.get('sourceServiceAccounts'))}


def instance_summary(instance):
    return {'name': instance.get('name') if instance.get('name') in ('actual-server', 'ugga-playtest-1', 'jazz-film-private') else alias(instance.get('name')),
            'status': instance.get('status'), 'zone': str(instance.get('zone', '')).rsplit('/', 1)[-1],
            'tags': [alias(t) for t in instance.get('tags', {}).get('items', [])],
            'interfaces': [{'network': alias(n.get('network')), 'external_ip_configured': bool(n.get('accessConfigs'))} for n in instance.get('networkInterfaces', [])],
            'accounts': [{'id': alias(s.get('email')), 'cloud_platform_scope': any(x.endswith('/cloud-platform') for x in s.get('scopes', []))} for s in instance.get('serviceAccounts', [])],
            'shielded': instance.get('shieldedInstanceConfig')}


def sql_summary(instance):
    settings = instance.get('settings', {})
    ip = settings.get('ipConfiguration', {})
    backup = settings.get('backupConfiguration', {})
    return {'id': alias(instance.get('name')), 'database_version': instance.get('databaseVersion'),
            'public_ip_enabled': ip.get('ipv4Enabled'), 'ssl_mode': ip.get('sslMode'), 'require_ssl': ip.get('requireSsl'),
            'authorized_networks': [n.get('value') for n in ip.get('authorizedNetworks', [])],
            'backups_enabled': backup.get('enabled'), 'pitr_enabled': backup.get('pointInTimeRecoveryEnabled'),
            'backup_retention': backup.get('backupRetentionSettings'),
            'deletion_protection': settings.get('deletionProtectionEnabled')}


def cloud_audit():
    """Bounded native GCP inventory through the existing deployment identity.

    No tokens, env values, startup scripts, SQL data, object contents or keys
    are requested. IAM member identities and infrastructure names are hashed.
    Every denied/disabled request remains an explicit coverage gap.
    """
    report = {'collected_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'projects': {}}
    for project in ('actual-budget-zb', 'parabolio-prod', 'voicecontrol-games'):
        sections = report['projects'][project] = {}
        def collect(label, parts, fields, transform=lambda x: x):
            state, value = command_json(parts, fields)
            if value is not None:
                state['result'] = transform(value)
            sections[label] = state
            return value
        collect('project_iam', ['projects', 'get-iam-policy', project], 'bindings', iam_summary)
        collect('instances', ['compute', 'instances', 'list', '--project=' + project],
                'name,status,zone,tags,networkInterfaces,serviceAccounts,shieldedInstanceConfig',
                lambda rows: [instance_summary(r) for r in rows])
        collect('firewalls', ['compute', 'firewall-rules', 'list', '--project=' + project],
                'name,direction,disabled,priority,allowed,denied,sourceRanges,sourceTags,sourceServiceAccounts,targetTags,targetServiceAccounts,network',
                lambda rows: [firewall_summary(r) for r in rows])
        collect('sql', ['sql', 'instances', 'list', '--project=' + project],
                'name,databaseVersion,settings.ipConfiguration,settings.backupConfiguration,settings.deletionProtectionEnabled',
                lambda rows: [sql_summary(r) for r in rows])
        buckets = collect('buckets', ['storage', 'buckets', 'list', '--project=' + project],
                          'name,iam_config,public_access_prevention,uniform_bucket_level_access,location,versioning_enabled,soft_delete_policy,retention_policy',
                          lambda rows: [{'id': alias(r.get('name')), 'iam_config': r.get('iam_config'),
                                         'public_access_prevention': r.get('public_access_prevention'),
                                         'uniform_bucket_level_access': r.get('uniform_bucket_level_access'),
                                         'versioning': r.get('versioning_enabled'), 'soft_delete_policy': r.get('soft_delete_policy')} for r in rows])
        for bucket in (buckets or [])[:30]:
            name = bucket.get('name', '')
            if re.fullmatch(r'[a-z0-9][a-z0-9._-]{1,221}[a-z0-9]', name):
                collect('bucket_iam_' + alias(name), ['storage', 'buckets', 'get-iam-policy', 'gs://' + name, '--project=' + project], 'bindings', iam_summary)
        for region in ('us-central1', 'us-west1'):
            services = collect('run_' + region, ['run', 'services', 'list', '--region=' + region, '--project=' + project],
                               'metadata.name,metadata.annotations."run.googleapis.com/ingress",metadata.annotations."run.googleapis.com/invoker-iam-disabled",status.url,status.latestReadyRevisionName,status.traffic,spec.template.spec.serviceAccountName,spec.template.spec.containers[].image',
                               lambda rows: [run_summary(r) for r in rows])
            for svc in (services or [])[:30]:
                name = svc.get('metadata', {}).get('name', '')
                if re.fullmatch(r'[a-z][a-z0-9-]{0,62}', name):
                    collect('run_iam_' + alias(name), ['run', 'services', 'get-iam-policy', name, '--region=' + region, '--project=' + project], 'bindings', iam_summary)
            collect('jobs_' + region, ['run', 'jobs', 'list', '--region=' + region, '--project=' + project],
                    'metadata.name,spec.template.spec.template.spec.containers[].image,spec.template.spec.template.spec.serviceAccountName',
                    lambda rows: [{'id': alias(r.get('metadata', {}).get('name')), 'template': sanitized_job(r)} for r in rows])
        functions = collect('functions', ['functions', 'list', '--project=' + project],
                            'name,status,runtime,ingressSettings,httpsTrigger.url,serviceAccountEmail,versionId,serviceConfig.uri,serviceConfig.ingressSettings,buildConfig.runtime',
                            lambda rows: [{'id': alias(r.get('name')), 'runtime': r.get('runtime') or r.get('buildConfig', {}).get('runtime'),
                                           'status': r.get('status'), 'ingress': r.get('ingressSettings') or r.get('serviceConfig', {}).get('ingressSettings'),
                                           'version': r.get('versionId')} for r in rows])
        for fn in (functions or [])[:30]:
            name = fn.get('name', '')
            match = re.fullmatch(r'projects/[^/]+/locations/([a-z0-9-]+)/functions/([a-zA-Z0-9_-]+)', name)
            if match:
                collect('function_iam_' + alias(name), ['functions', 'get-iam-policy', match[2], '--region=' + match[1], '--project=' + project], 'bindings', iam_summary)
        sections['bounded_enumeration'] = {'status': 'observed', 'result': {'bucket_iam_truncated': len(buckets or []) > 30, 'function_iam_truncated': len(functions or []) > 30}}
        provider = 'github/providers/personal-site' if project == 'parabolio-prod' else 'github-actions/providers/uncle-ugga'
        if project != 'actual-budget-zb':
            pool, provider_id = provider.split('/providers/')
            collect('wif', ['iam', 'workload-identity-pools', 'providers', 'describe', provider_id,
                            '--workload-identity-pool=' + pool, '--location=global', '--project=' + project],
                    'attributeCondition,state,disabled', lambda r: {'state': r.get('state'), 'disabled': r.get('disabled'),
                        'repository_restricted': 'assertion.repository' in r.get('attributeCondition', ''),
                        'main_restricted': 'refs/heads/main' in r.get('attributeCondition', '')})
    return report


def run_summary(service):
    meta = service.get('metadata', {})
    annotations = meta.get('annotations', {})
    status = service.get('status', {})
    spec = service.get('spec', {}).get('template', {}).get('spec', {})
    return {'name': meta.get('name') if meta.get('name') in ('jazz-api', 'workbench', 'ugga-allocator', 'ugga-telemetry-admin', 'ugga-telemetry-ingest') else alias(meta.get('name')),
            'ingress': annotations.get('run.googleapis.com/ingress'),
            'invoker_iam_disabled': annotations.get('run.googleapis.com/invoker-iam-disabled'),
            'latest_ready_revision': status.get('latestReadyRevisionName'),
            'traffic': [{'revision': t.get('revisionName'), 'percent': t.get('percent')} for t in status.get('traffic', [])],
            'account': alias(spec.get('serviceAccountName')),
            'images': [c.get('image') for c in spec.get('containers', [])]}


def sanitized_job(job):
    spec = job.get('spec', {}).get('template', {}).get('spec', {}).get('template', {}).get('spec', {})
    return {'account': alias(spec.get('serviceAccountName')), 'images': [c.get('image') for c in spec.get('containers', [])]}


def listener_summary(text):
    result = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) < 6 or parts[0] not in ('tcp', 'udp'):
            continue
        host, sep, port = parts[4].rpartition(':')
        if not sep or not port.isdigit():
            continue
        host = host.strip('[]').split('%')[0]
        try:
            addr = ipaddress.ip_address(host)
            binding = 'wildcard' if addr.is_unspecified else 'loopback' if addr.is_loopback else 'private' if addr.is_private else 'public'
        except ValueError:
            binding = 'unknown'
        processes = [p for p in re.findall(r'\("([^"\s]+)"', line) if re.fullmatch(r'[A-Za-z0-9_.-]{1,80}', p)]
        result.append({'protocol': parts[0], 'port': int(port), 'binding': binding, 'processes': processes})
    return result


def host_audit():
    report = {'collected_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'runtime': inventory()}
    sockets = output(['ss', '-H', '-lntup'])
    report['listeners'] = {'status': 'observed', 'result': listener_summary(sockets)} if sockets is not None else {'status': 'unavailable'}
    units = output(['systemctl', 'list-units', '--type=service', '--state=running', '--no-legend', '--plain'])
    names = [line.split()[0] for line in (units or '').splitlines() if line.split() and re.fullmatch(r'[A-Za-z0-9@_.-]+\.service', line.split()[0])]
    report['running_services'] = names if units is not None else None
    packages = output(['dpkg-query', '-W', '-f=${Package}\t${Version}\n'])
    pattern = r'(?:caddy|docker.*|containerd.*|runc|openssh.*|openssl|libssl.*|libxml2.*|ffmpeg|python3.*|nodejs|npm|linux-image.*|unattended-upgrades|fail2ban|ufw|nginx.*|apache2.*|php.*|plex.*|collabora.*|code-brand.*)'
    report['selected_packages'] = dict(line.split('\t', 1) for line in (packages or '').splitlines() if '\t' in line and re.fullmatch(pattern, line.split('\t')[0])) if packages is not None else None
    ssh = output(['/usr/sbin/sshd', '-T'])
    keys = ('permitrootlogin', 'passwordauthentication', 'pubkeyauthentication', 'permitemptypasswords', 'maxauthtries', 'allowtcpforwarding', 'gatewayports', 'x11forwarding')
    report['ssh_policy'] = dict(line.split(' ', 1) for line in (ssh or '').splitlines() if line.split(' ', 1)[0] in keys) if ssh is not None else None
    docker = output(['docker', 'ps', '--format', '{{json .Names}} {{json .Image}}'])
    report['containers'] = [{'name': alias(line.split(' ', 1)[0]), 'image': line.split(' ', 1)[1].strip('"')} for line in (docker or '').splitlines() if ' ' in line] if docker is not None else None
    upgrades = output(['apt', 'list', '--upgradable'])
    report['cached_upgrade_candidates'] = [line for line in (upgrades or '').splitlines() if '/' in line and '[upgradable from:' in line] if upgrades is not None else None
    try:
        report['apt_cache_mtime_utc'] = datetime.datetime.fromtimestamp(Path('/var/lib/apt/lists').stat().st_mtime, datetime.timezone.utc).isoformat()
    except OSError:
        report['apt_cache_mtime_utc'] = None
    report['caddy_hosts'] = []
    try:
        # Hostnames only; never adapt/log the config or read service env files.
        config = Path('/etc/caddy/Caddyfile').read_text()
        report['caddy_hosts'] = sorted(set(re.findall(r'(?m)^([A-Za-z0-9][A-Za-z0-9., -]*\.[A-Za-z][A-Za-z0-9., -]*)\s*\{\s*$', config)))
    except OSError:
        report['caddy_hosts'] = None
    return report


if __name__ == '__main__':
    # A fresh gcloud SSH session can print key-generation banners to stdout.
    mode = sys.argv[1:]
    if mode == ['--cloud-audit']:
        print('CLOUD_SECURITY_INVENTORY=' + json.dumps(cloud_audit(), sort_keys=True))
    elif mode == ['--host-audit']:
        print('HOST_SECURITY_INVENTORY=' + json.dumps(host_audit(), sort_keys=True))
    elif not mode:
        print('SITE_RUNTIME_INVENTORY=' + json.dumps(inventory(), sort_keys=True))
    else:
        raise SystemExit('Expected --cloud-audit, --host-audit or no argument')
