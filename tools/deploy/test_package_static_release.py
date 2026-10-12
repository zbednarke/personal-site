"""deploy/package-static-release.py: a release built from the changed files
only is the same as one built from the full archive. Both go through the real
deploy/publish-static-release.sh against a temporary site root."""
import filecmp
import os
import random
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PACKAGE = ROOT / 'deploy' / 'package-static-release.py'
PUBLISH = ROOT / 'deploy' / 'publish-static-release.sh'
PATHS = ['index.html', '.nojekyll', 'assets', 'jazz', 'trumpets', 'commonplace', 'privacy']
# The files publish-static-release.sh requires, plus a few more.
FILES = {
    'index.html': 'home', '.nojekyll': '', 'jazz/index.html': 'jazz', 'assets/jazz/app.js': 'app',
    'trumpets/index.html': 'trumpets', 'commonplace/index.html': 'commonplace', 'assets/commonplace/app.js': 'cp',
    'assets/workbench/workbench.js': 'wb', 'assets/trumpets/taste.js': 'taste v1', 'privacy/index.html': 'privacy',
    # Incompressible, like the real 1.7 MB PNG.
    'assets/jazz/og.png': random.Random(7).randbytes(200_000),
}


def write(base, files):
    for name, body in files.items():
        path = Path(base, name); path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(body) if isinstance(body, bytes) else path.write_text(body)


def tree(base):
    out = {}
    for root, _, names in os.walk(base):
        for name in names:
            path = Path(root, name); rel = str(path.relative_to(base))
            if rel != '.release-commit': out[rel] = path.read_bytes()
    return out


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp()); self.addCleanup(shutil.rmtree, self.tmp)
        # The commit: one file changed, one new; the rest as live.
        self.repo = self.tmp / 'repo'
        write(self.repo, {**FILES, 'assets/trumpets/taste.js': 'taste v2', 'assets/workbench/fixlist.js': 'new'})
        # The live release: the previous commit plus a file the repository does
        # not manage (kept by the overlay).
        self.site = self.tmp / 'site'
        live = self.site / 'releases' / 'old'
        write(live, {**FILES, 'films-note.txt': 'not managed'})
        (self.site / 'current').symlink_to(live)

    def manifest(self, site):
        out = subprocess.run('cd current && find ' + ' '.join(PATHS) + ' -type f -exec sha256sum {} + 2>/dev/null; true',
                             shell=True, cwd=site, capture_output=True, text=True, check=True).stdout
        path = self.tmp / ('live-%s.sha256' % site.name); path.write_text(out); return path

    def package(self, live, out):
        result = subprocess.run(['python3', str(PACKAGE), '--live', str(live), '--out', str(out), *PATHS],
                                cwd=self.repo, capture_output=True, text=True, check=True)
        with tarfile.open(out) as archive: return sorted(archive.getnames()), result.stdout

    def publish(self, site, archive, sha):
        subprocess.run(['bash', str(PUBLISH), str(archive), sha], env={**os.environ, 'SITE_ROOT': str(site)},
                       capture_output=True, text=True, check=True)
        return tree(os.path.realpath(site / 'current'))

    def test_delta_holds_only_new_and_changed_files(self):
        names, summary = self.package(self.manifest(self.site), self.tmp / 'delta.tar.gz')
        self.assertEqual(names, ['assets/trumpets/taste.js', 'assets/workbench/fixlist.js'])
        self.assertIn('2 of 12 files packaged (10 already live)', summary)

    def test_delta_and_full_archive_publish_the_same_release(self):
        full_site = self.tmp / 'site-full'; shutil.copytree(self.site, full_site, symlinks=True)
        (full_site / 'current').unlink(); (full_site / 'current').symlink_to(full_site / 'releases' / 'old')
        full = self.tmp / 'full.tar.gz'
        subprocess.run(['tar', '-czf', str(full), *PATHS], cwd=self.repo, check=True)
        delta = self.tmp / 'delta.tar.gz'; self.package(self.manifest(self.site), delta)
        self.assertLess(delta.stat().st_size, full.stat().st_size / 20)
        a = self.publish(full_site, full, 'a' * 40)
        b = self.publish(self.site, delta, 'b' * 40)
        self.assertEqual(a, b)
        self.assertEqual(b['assets/trumpets/taste.js'], b'taste v2')
        self.assertEqual(b['films-note.txt'], b'not managed')

    def test_without_live_hashes_every_file_is_packaged(self):
        for live in (self.tmp / 'missing.sha256', self.tmp / 'empty.sha256'):
            if live.name == 'empty.sha256': live.write_text('')
            names, _ = self.package(live, self.tmp / 'all.tar.gz')
            self.assertEqual(len(names), 12)

    def test_noise_and_wrong_hashes_never_leave_a_file_out(self):
        live = self.manifest(self.site)
        text = live.read_text()
        # gcloud chatter, a CRLF line, and a stale hash for index.html.
        lines = ['Warning: Permanently added host', *text.splitlines()]
        lines = [l.replace(l[:64], '0' * 64) if l.endswith('  index.html') else l for l in lines]
        live.write_text('\r\n'.join(lines))
        names, _ = self.package(live, self.tmp / 'delta.tar.gz')
        self.assertEqual(names, ['assets/trumpets/taste.js', 'assets/workbench/fixlist.js', 'index.html'])

    def test_nothing_changed_gives_an_empty_archive_that_publishes(self):
        repo_files = tree(self.repo)
        write(self.site / 'releases' / 'old', repo_files)
        delta = self.tmp / 'delta.tar.gz'
        names, _ = self.package(self.manifest(self.site), delta)
        self.assertEqual(names, [])
        published = self.publish(self.site, delta, 'c' * 40)
        self.assertEqual({k: v for k, v in published.items() if k != 'films-note.txt'}, repo_files)


if __name__ == '__main__':
    unittest.main()
