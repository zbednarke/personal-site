"""deploy/upload-static-release.sh: the release files reach the VM byte for byte
over an ssh session's stdin, a stalled attempt is cut off and retried, bytes
that do not match are never unpacked, and publish re-checks the files before
sudo. A fake `gcloud compute ssh` runs the remote command locally with the
same stdin (sudo runs the command as is); FAKE_MODE makes calls stall, corrupt
the stream or hang after the command."""
import hashlib
import os
import random
import re
import shutil
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
UPLOAD = ROOT / 'deploy' / 'upload-static-release.sh'
PUBLISH = ROOT / 'deploy' / 'publish-static-release.sh'
REQUIRED = ('index.html', 'jazz/index.html', 'assets/jazz/app.js', 'trumpets/index.html', 'commonplace/index.html',
            'assets/commonplace/app.js', 'assets/workbench/workbench.js')

# Like gcloud compute ssh VM ... --command CMD: CMD runs in a shell with the
# caller's stdin. FAKE_MODE is a comma list, one entry per call (the last one
# repeats): ok; stall (reads nothing and hangs); corrupt (flips a byte);
# hang-after (runs the command, then never closes, like scp's ~240 s wait).
FAKE_GCLOUD = r'''#!/usr/bin/env bash
cmd=
while [[ $# -gt 0 ]]; do
  case $1 in --command) cmd=$2; shift 2 ;; --command=*) cmd=${1#--command=}; shift ;; *) shift ;; esac
done
n=$(( $(cat "$FAKE_STATE/calls" 2>/dev/null || echo 0) + 1 )); echo $n > "$FAKE_STATE/calls"
printf '%s' "$cmd" > "$FAKE_STATE/command.$n"
IFS=, read -ra modes <<<"$FAKE_MODE"
mode=${modes[$((n - 1))]:-${modes[-1]}}
sudo() { "$@"; }; export -f sudo
case $mode in
  ok) exec bash -c "$cmd" ;;
  stall) exec sleep 60 ;;
  corrupt) python3 -c 'import sys; b = bytearray(sys.stdin.buffer.read()); b[len(b) // 2] ^= 1; sys.stdout.buffer.write(b)' | bash -c "$cmd" ;;
  hang-after) bash -c "$cmd"; exec sleep 60 ;;
esac
'''


class UploadTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp()); self.addCleanup(shutil.rmtree, self.tmp)
        self.state = self.tmp / 'state'; self.state.mkdir()
        self.gcloud = self.tmp / 'gcloud'; self.gcloud.write_text(FAKE_GCLOUD); self.gcloud.chmod(0o755)
        self.archive = self.tmp / 'site-abc.tar.gz'
        self.archive.write_bytes(random.Random(3).randbytes(40_000))  # bigger than one 16 KB IAP frame
        self.remote = self.tmp / 'vm' / 'site-release-abc'

    def run_script(self, mode, args, env):
        env = {**os.environ, 'FAKE_MODE': mode, 'FAKE_STATE': str(self.state), **env}
        env = {k: v for k, v in env.items() if v is not None}
        (self.state / 'calls').unlink(missing_ok=True)
        start = time.monotonic()
        result = subprocess.run(
            ['bash', str(UPLOAD), *args, '--', str(self.gcloud), 'compute', 'ssh', 'vm', '--tunnel-through-iap', '--quiet'],
            env=env, capture_output=True, text=True, timeout=120)
        result.elapsed = time.monotonic() - start
        calls = self.state / 'calls'
        result.calls = int(calls.read_text()) if calls.exists() else 0
        return result

    def upload(self, mode, timeout=3, attempts=2):
        return self.run_script(mode, ['upload', str(self.remote), str(self.archive), str(PUBLISH)],
                               {'UPLOAD_TIMEOUT': None if timeout is None else str(timeout),
                                'UPLOAD_ATTEMPTS': str(attempts)})

    def publish(self, mode, site, sha='d' * 40, timeout=3):
        return self.run_script(mode, ['publish', str(self.remote), str(self.archive), str(PUBLISH), sha],
                               {'PUBLISH_TIMEOUT': str(timeout), 'SITE_ROOT': str(site)})

    def make_site(self):
        """A live release, and a real archive as package-static-release.py writes it."""
        site = self.tmp / 'site'; live = site / 'releases' / 'old'
        for name in REQUIRED:
            (live / name).parent.mkdir(parents=True, exist_ok=True); (live / name).write_text(name)
        (site / 'current').symlink_to(live)
        src = self.tmp / 'src'; (src / 'assets').mkdir(parents=True); (src / 'assets' / 'new.js').write_text('new')
        subprocess.run(['tar', '-czf', str(self.archive), 'assets'], cwd=src, check=True)
        return site

    def assertUploaded(self):
        self.assertEqual((self.remote / 'site-abc.tar.gz').read_bytes(), self.archive.read_bytes())
        self.assertEqual((self.remote / 'publish-static-release.sh').read_bytes(), PUBLISH.read_bytes())
        self.assertEqual(sorted(p.name for p in self.remote.iterdir()), ['publish-static-release.sh', 'site-abc.tar.gz'])

    # Upload

    def test_files_arrive_byte_for_byte_in_one_session(self):
        r = self.upload('ok')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 1)
        self.assertRegex(r.stdout, r'Upload attempt 1: \d+ bytes in \d+ s \(VM received \d+ bytes in \d+ ms\); '
                                   r'sha256 [0-9a-f]{64} verified on the VM')
        self.assertUploaded()
        self.assertEqual(oct(self.remote.stat().st_mode & 0o777), '0o700')

    def test_an_existing_remote_dir_is_made_private(self):
        self.remote.mkdir(parents=True); self.remote.chmod(0o777)
        self.assertEqual(self.upload('ok').returncode, 0)
        self.assertEqual(oct(self.remote.stat().st_mode & 0o777), '0o700')

    def test_a_stalled_attempt_is_cut_off_and_retried(self):
        r = self.upload('stall,ok', timeout=2)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 2)
        self.assertIn('Upload attempt 1 of 2 failed after 2 s: timed out after 2 s (VM reported nothing)', r.stderr)
        self.assertIn('Upload attempt 2:', r.stdout)
        self.assertLess(r.elapsed, 20)
        self.assertUploaded()

    def test_a_session_that_hangs_after_the_verified_line_is_done(self):
        # The VM confirmed the hash and unpacked the bundle; only the close hangs.
        r = self.upload('hang-after,stall', timeout=2)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 1)
        self.assertIn('the session did not close; timed out after 2 s', r.stdout)
        self.assertUploaded()

    def test_a_session_that_hangs_before_the_verified_line_is_retried(self):
        # The first session hangs before the VM prints the verified line.
        r = self.upload('stall,hang-after', timeout=2)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 2)
        self.assertIn('Upload attempt 1 of 2 failed after 2 s: timed out after 2 s', r.stderr)
        self.assertUploaded()

    def test_timeout_scales_with_the_bundle_and_is_capped(self):
        for size, capped in ((40_000, False), (4_000_000, True)):
            self.archive.write_bytes(random.Random(size).randbytes(size))
            r = self.upload('ok', timeout=None)
            self.assertEqual(r.returncode, 0, r.stderr)
            m = re.search(r'Uploading (\d+) bytes \(sha256 [0-9a-f]{64}\), timeout (\d+) s per attempt, 2 attempts', r.stdout)
            self.assertIsNotNone(m, r.stdout)
            bundle, timeout = int(m.group(1)), int(m.group(2))
            self.assertEqual(timeout, 900 if capped else 120 + bundle // 4096)
        # The 2.46 MB full archive gets about 720 s.
        self.assertEqual(120 + 2_460_000 // 4096, 720)

    def test_corrupted_bytes_are_never_unpacked_and_fail_the_upload(self):
        r = self.upload('corrupt,corrupt,stall')
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.calls, 3)  # two attempts, then the cleanup (which stalls here)
        self.assertIn('sha256 mismatch on the VM', r.stderr)
        self.assertIn('Upload failed after 2 attempts; not publishing.', r.stderr)
        self.assertEqual(list(self.remote.iterdir()), [])

    def test_a_failed_upload_removes_the_remote_dir(self):
        r = self.upload('corrupt')
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.calls, 3)
        self.assertFalse(self.remote.exists())

    def test_a_corrupted_first_attempt_is_retried(self):
        r = self.upload('corrupt,ok')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn('Upload attempt 1 of 2 failed', r.stderr)
        self.assertUploaded()

    def test_every_attempt_stalling_fails_within_the_timeouts(self):
        r = self.upload('stall', timeout=2)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.calls, 3)
        self.assertLess(r.elapsed, 20)  # 2 x 2 s, and the cleanup is capped the same way
        self.assertFalse((self.remote / 'site-abc.tar.gz').exists())

    def test_a_symlinked_remote_dir_is_refused(self):
        elsewhere = self.tmp / 'elsewhere'; elsewhere.mkdir()
        self.remote.parent.mkdir(parents=True); self.remote.symlink_to(elsewhere)
        r = self.upload('ok')
        self.assertEqual(r.returncode, 1)
        self.assertIn('is a symlink; not uploading', r.stderr)
        self.assertEqual(list(elsewhere.iterdir()), [])
        self.assertTrue(self.remote.is_symlink())  # the cleanup leaves it alone

    @unittest.skipUnless(os.geteuid() == 0, 'needs root to give a dir to another user')
    def test_a_remote_dir_owned_by_someone_else_is_refused(self):
        self.remote.mkdir(parents=True); os.chown(self.remote, 65534, 65534)
        r = self.upload('ok')
        self.assertEqual(r.returncode, 1)
        self.assertIn('is not ours; not uploading', r.stderr)
        self.assertEqual(list(self.remote.iterdir()), [])
        self.assertTrue(self.remote.exists())  # the cleanup leaves it alone

    def test_rejects_a_remote_dir_that_would_need_quoting(self):
        for bad in ("/tmp/x'; rm -rf ~", '/tmp/../etc'):
            r = subprocess.run(['bash', str(UPLOAD), 'upload', bad, str(self.archive), '--', str(self.gcloud)],
                               capture_output=True, text=True)
            self.assertEqual(r.returncode, 2)
        self.assertFalse((self.state / 'calls').exists())

    # Publish

    def test_uploaded_files_publish_and_the_remote_dir_is_removed(self):
        site = self.make_site()
        self.assertEqual(self.upload('ok').returncode, 0)
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn('published ' + 'd' * 40, r.stdout)
        self.assertEqual((site / 'current' / 'assets' / 'new.js').read_text(), 'new')
        self.assertFalse(self.remote.exists())
        # The runner's hashes are literals in the command, checked before sudo.
        command = (self.state / 'command.1').read_text()
        self.assertIn(hashlib.sha256(PUBLISH.read_bytes()).hexdigest(), command)
        self.assertIn(hashlib.sha256(self.archive.read_bytes()).hexdigest(), command)
        self.assertLess(command.index('sha256sum -c'), command.index('sudo bash'))
        self.assertLess(command.index('[ -L "$d" ]'), command.index('sudo bash'))

    def test_publish_refuses_a_changed_publish_script(self):
        site = self.make_site(); before = os.path.realpath(site / 'current')
        self.assertEqual(self.upload('ok').returncode, 0)
        script = self.remote / 'publish-static-release.sh'
        script.write_text(script.read_text() + '\ntouch "$SITE_ROOT/pwned"\n')
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 1)
        self.assertIn("are not the runner's; not publishing", r.stderr)
        self.assertEqual(os.path.realpath(site / 'current'), before)
        self.assertFalse((site / 'pwned').exists())
        self.assertFalse(self.remote.exists())

    def test_publish_refuses_a_changed_archive(self):
        site = self.make_site(); before = os.path.realpath(site / 'current')
        self.assertEqual(self.upload('ok').returncode, 0)
        with open(self.remote / 'site-abc.tar.gz', 'ab') as f: f.write(b'x')
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 1)
        self.assertIn("are not the runner's; not publishing", r.stderr)
        self.assertEqual(os.path.realpath(site / 'current'), before)

    def test_publish_refuses_a_symlinked_or_missing_dir(self):
        site = self.make_site(); before = os.path.realpath(site / 'current')
        self.assertEqual(self.upload('ok').returncode, 0)
        real = self.tmp / 'real'; self.remote.rename(real); self.remote.symlink_to(real)
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 1)
        self.assertIn('is missing, a symlink or not ours; not publishing', r.stderr)
        self.assertEqual(os.path.realpath(site / 'current'), before)
        self.assertTrue((real / 'publish-static-release.sh').exists())  # never removed through a link
        self.remote.unlink()
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 1)
        self.assertIn('is missing, a symlink or not ours; not publishing', r.stderr)

    @unittest.skipUnless(os.geteuid() == 0, 'needs root to give a dir to another user')
    def test_publish_refuses_a_dir_owned_by_someone_else(self):
        site = self.make_site(); before = os.path.realpath(site / 'current')
        self.assertEqual(self.upload('ok').returncode, 0)
        os.chown(self.remote, 65534, 65534)
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 1)
        self.assertIn('is missing, a symlink or not ours; not publishing', r.stderr)
        self.assertEqual(os.path.realpath(site / 'current'), before)

    def test_a_failed_publish_still_removes_the_remote_dir(self):
        site = self.make_site()
        (site / 'current').unlink(); (site / 'current').mkdir()  # not a symlink: the publish script refuses
        self.assertEqual(self.upload('ok').returncode, 0)
        r = self.publish('ok', site)
        self.assertEqual(r.returncode, 1)
        self.assertIn('is not a symlink; refusing to switch releases', r.stderr)
        self.assertFalse(self.remote.exists())

    def test_a_publish_session_that_hangs_after_publishing_is_done(self):
        site = self.make_site()
        self.assertEqual(self.upload('ok').returncode, 0)
        r = self.publish('hang-after', site, timeout=2)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn('did not close; timed out after 2 s, but the VM reported the release published', r.stdout)
        self.assertEqual((site / 'current' / 'assets' / 'new.js').read_text(), 'new')

    def test_a_publish_that_times_out_without_publishing_fails(self):
        site = self.make_site()
        self.assertEqual(self.upload('ok').returncode, 0)
        r = self.publish('stall', site, timeout=2)
        self.assertEqual(r.returncode, 1)
        self.assertIn('Publish timed out after 2 s without the VM reporting the release published', r.stderr)


if __name__ == '__main__':
    unittest.main()
