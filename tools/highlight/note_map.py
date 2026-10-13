"""Render portable note maps and refresh published pages without touching media."""
import argparse
from datetime import date as Date
import json
import os
from pathlib import Path
import re
import shutil
import tempfile

HERE = Path(__file__).resolve().parent
PUBLISHED = re.compile(r'(?P<date>\d{4}-\d{2}-\d{2})-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\.analysis\.json')


def validated_date(value):
    if value is None:
        return None
    if not isinstance(value, str) or not re.fullmatch(r'\d{4}-\d{2}-\d{2}', value):
        raise ValueError('Expected a YYYY-MM-DD practice date')
    return Date.fromisoformat(value).isoformat()


def embedded_json(value):
    # Raw-text script elements end at </script>, even for application/json.
    return json.dumps(value, ensure_ascii=False, allow_nan=False).replace('<', '\\u003c')


def render_note_map(maps, date=None, template=None):
    """Omit per-frame arrays; preserve note events, summaries and source order."""
    compact = {rid: {key: value for key, value in analysis.items() if key != 'frames'}
               for rid, analysis in maps.items()}
    if template is None:
        template = (HERE / 'note-map.html').read_text(encoding='utf-8')
    # Replace placeholders in one pass so literal placeholder text inside titles
    # cannot accidentally be replaced on a subsequent substitution.
    values = {'__ANALYSIS__': embedded_json(compact),
              '__METADATA__': embedded_json({'date': validated_date(date)})}
    return re.sub(r'__ANALYSIS__|__METADATA__', lambda match: values[match.group()], template)


def regenerate(directory, template=None):
    """Refresh known published maps only, keeping the first HTML backup."""
    updated = []
    for source in sorted(Path(directory).glob('*.analysis.json')):
        match = PUBLISHED.fullmatch(source.name)
        if not match or source.is_symlink():
            continue
        try:
            day = validated_date(match['date'])
        except ValueError:
            continue
        target = source.with_name(source.name.removesuffix('.analysis.json') + '.notes.html')
        if not target.is_file() or target.is_symlink():
            continue
        html = render_note_map(json.loads(source.read_text(encoding='utf-8')), day, template)
        backup = target.with_name(target.name + '.bak')
        # Exclusive creation prevents clobbering a previous recovery copy.
        try:
            with backup.open('xb') as handle, target.open('rb') as original:
                shutil.copyfileobj(original, handle)
            backup.chmod(target.stat().st_mode & 0o777)
        except FileExistsError:
            pass
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=target.parent,
                                             prefix='.' + target.name, suffix='.tmp', delete=False) as handle:
                temporary = Path(handle.name)
                handle.write(html)
                handle.flush()
                os.fsync(handle.fileno())
            temporary.chmod(target.stat().st_mode & 0o777)
            os.replace(temporary, target)
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
        updated.append(target)
    return updated


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path, help='Directory containing published analysis and note-map pairs')
    args = parser.parse_args()
    for path in regenerate(args.directory):
        print(path.name)
