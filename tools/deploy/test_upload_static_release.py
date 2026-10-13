"""deploy/upload-static-release.sh: the release files reach the VM byte for byte
over an ssh session's stdin, a stalled attempt is cut off and retried, and
bytes that do not match are never unpacked. A fake `gcloud compute ssh` runs
the remote command locally with the same stdin; FAKE_MODE makes attempts
stall or corrupt the stream."""
import os
import random
import shutil
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
UPLOAD = ROOT / 'deploy' / 'upload-static-release.sh'
PUBLISH = ROOT / 'deploy' / 'publish-static-release.sh'

# Like gcloud compute ssh VM ... --command CMD: CMD runs in a shell with the
# caller's stdin. FAKE_MODE is a comma list, one entry per call (the last one
# repeats): ok, stall (sends nothing and hangs), corrupt (flips a byte), hang-after
# (runs the command, then never closes, like scp's ~240 s wait).
FAKE_GCLOUD = r'''#!/usr/bin/env bash
cmd=
while [[ $# -gt 0 ]]; do
  case $1 in --command) cmd=$2; shift 2 ;; --command=*) cmd=${1#--command=}; shift ;; *) shift ;; esac
done
n=$(( $(cat "$FAKE_STATE/calls" 2>/dev/null || echo 0) + 1 )); echo $n > "$FAKE_STATE/calls"
IFS=, read -ra modes <<<"$FAKE_MODE"
mode=${modes[$((n - 1))]:-${modes[-1]}}
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

    def upload(self, mode, timeout=3, attempts=2):
        env = {**os.environ, 'FAKE_MODE': mode, 'FAKE_STATE': str(self.state),
               'UPLOAD_TIMEOUT': str(timeout), 'UPLOAD_ATTEMPTS': str(attempts)}
        start = time.monotonic()
        result = subprocess.run(
            ['bash', str(UPLOAD), str(self.remote), str(self.archive), str(PUBLISH),
             '--', str(self.gcloud), 'compute', 'ssh', 'vm', '--tunnel-through-iap', '--quiet'],
            env=env, capture_output=True, text=True, timeout=120)
        result.elapsed = time.monotonic() - start
        result.calls = int((self.state / 'calls').read_text())
        return result

    def assertUploaded(self):
        self.assertEqual((self.remote / 'site-abc.tar.gz').read_bytes(), self.archive.read_bytes())
        self.assertEqual((self.remote / 'publish-static-release.sh').read_bytes(), PUBLISH.read_bytes())
        self.assertEqual(sorted(p.name for p in self.remote.iterdir()), ['publish-static-release.sh', 'site-abc.tar.gz'])

    def test_files_arrive_byte_for_byte_in_one_session(self):
        r = self.upload('ok')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 1)
        self.assertRegex(r.stdout, r'Upload attempt 1: \d+ bytes in \d+ s \(VM received \d+ bytes in \d+ ms\); sha256 [0-9a-f]{64} verified on the VM')
        self.assertUploaded()
        self.assertEqual(oct(self.remote.stat().st_mode & 0o777), '0o700')

    def test_a_stalled_attempt_is_cut_off_and_retried(self):
        r = self.upload('stall,ok', timeout=2)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 2)
        self.assertIn('Upload attempt 1 of 2 failed after 2 s: timed out after 2 s', r.stderr)
        self.assertIn('Upload attempt 2:', r.stdout)
        self.assertLess(r.elapsed, 20)
        self.assertUploaded()

    def test_a_session_that_never_closes_is_retried(self):
        # The bytes arrive and are unpacked, but the session hangs on close.
        r = self.upload('hang-after,ok', timeout=2)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.calls, 2)
        self.assertUploaded()

    def test_corrupted_bytes_are_never_unpacked_and_fail_the_upload(self):
        r = self.upload('corrupt')
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.calls, 2)
        self.assertIn('sha256 mismatch on the VM', r.stderr)
        self.assertIn('Upload failed after 2 attempts; not publishing.', r.stderr)
        self.assertEqual(list(self.remote.iterdir()), [])

    def test_a_corrupted_first_attempt_is_retried(self):
        r = self.upload('corrupt,ok')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn('Upload attempt 1 of 2 failed', r.stderr)
        self.assertUploaded()

    def test_every_attempt_stalling_fails_within_the_timeouts(self):
        r = self.upload('stall', timeout=2)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.calls, 2)
        self.assertLess(r.elapsed, 20)
        self.assertFalse((self.remote / 'site-abc.tar.gz').exists())

    def test_uploaded_files_publish(self):
        self.assertEqual(self.upload('ok').returncode, 0)
        site = self.tmp / 'site'; live = site / 'releases' / 'old'
        for name in ('index.html', 'jazz/index.html', 'assets/jazz/app.js', 'trumpets/index.html', 'commonplace/index.html',
                     'assets/commonplace/app.js', 'assets/workbench/workbench.js'):
            (live / name).parent.mkdir(parents=True, exist_ok=True); (live / name).write_text(name)
        (site / 'current').symlink_to(live)
        # A real archive this time, as package-static-release.py writes it.
        src = self.tmp / 'src'; (src / 'assets').mkdir(parents=True); (src / 'assets' / 'new.js').write_text('new')
        subprocess.run(['tar', '-czf', str(self.archive), 'assets'], cwd=src, check=True)
        (self.state / 'calls').unlink()
        self.assertEqual(self.upload('ok').returncode, 0)
        subprocess.run(['bash', str(self.remote / 'publish-static-release.sh'), str(self.remote / 'site-abc.tar.gz'), 'd' * 40],
                       env={**os.environ, 'SITE_ROOT': str(site)}, check=True, capture_output=True)
        self.assertEqual((site / 'current' / 'assets' / 'new.js').read_text(), 'new')

    def test_rejects_a_remote_dir_that_would_need_quoting(self):
        r = subprocess.run(['bash', str(UPLOAD), "/tmp/x'; rm -rf ~", str(self.archive), '--', str(self.gcloud)],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)
        self.assertFalse((self.state / 'calls').exists())


if __name__ == '__main__':
    unittest.main()
