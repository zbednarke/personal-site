"""Build the exact bundle consumed by install-film-editor.py; never include media."""
import argparse
import hashlib
import importlib.util
from pathlib import Path
import tarfile

ROOT=Path(__file__).resolve().parent.parent

def package(output):
    installer=ROOT/'deploy/install-film-editor.py'
    spec=importlib.util.spec_from_file_location('film_installer',installer)
    module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
    files=[p for p in (ROOT/'tools/highlight').iterdir() if p.is_file() and p.suffix in ('.py','.html','.txt')]
    files += [installer]+[ROOT/p for p in module.FRONTEND]
    output.parent.mkdir(parents=True,exist_ok=True)
    with tarfile.open(output,'w:gz') as bundle:
        for file in sorted(files):bundle.add(file,arcname=str(file.relative_to(ROOT)))
    digest=hashlib.sha256(output.read_bytes()).hexdigest()
    print(str(output)+'  sha256='+digest)

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,default=ROOT/'.local/film-editor-release.tar.gz')
    package(parser.parse_args().output)
