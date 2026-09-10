"""Trusted validation between the browser, editing agent and renderer."""
import math
import re
from urllib.parse import urlparse


def number(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def media_url(value):
    parsed = urlparse(value)
    if parsed.scheme != 'https' or parsed.hostname != 'storage.googleapis.com' or parsed.port not in (None, 443) or parsed.username or parsed.password:
        raise ValueError('Media must use signed Google Storage URLs')
    return value


def snapshot(payload):
    if not isinstance(payload, dict) or not re.fullmatch(r'\d{4}-\d{2}-\d{2}', str(payload.get('date', ''))):
        raise ValueError('Choose a practice date')
    candidates, recordings = payload.get('candidates'), payload.get('recordings')
    if not isinstance(candidates, list) or not 1 <= len(candidates) <= 500 or not isinstance(recordings, list) or len(recordings) > 100:
        raise ValueError('Provide 1–500 existing candidate moments')
    sources = {}
    for r in recordings:
        if not isinstance(r, dict):
            raise ValueError('Invalid recording')
        rid = str(r.get('id', ''))
        if not re.fullmatch(r'[a-zA-Z0-9_-]{1,80}', rid) or rid in sources or not number(r.get('durationMs')) or r['durationMs'] <= 0:
            raise ValueError('Invalid or duplicate recording')
        sources[rid] = dict(id=rid, durationMs=r['durationMs'], title=str(r.get('title', 'Practice'))[:200],
                            audioUrl=media_url(r.get('audioUrl', '')), videoUrl=media_url(r['videoUrl']) if r.get('videoUrl') else None)
    result, ids = [], set()
    for c in candidates:
        if not isinstance(c, dict):
            raise ValueError('Invalid candidate')
        if c.get('reviewStatus') == 'rejected':
            continue
        cid, rid = str(c.get('id', '')), str(c.get('recordingId', ''))
        if not re.fullmatch(r'[a-zA-Z0-9_-]{1,80}', cid) or cid in ids or rid not in sources:
            raise ValueError('Invalid candidate reference')
        start, end = c.get('startMs'), c.get('endMs')
        if not number(start) or not number(end) or not 0 <= start < end <= sources[rid]['durationMs'] or end-start < 500:
            raise ValueError('Candidate boundary is outside its recording')
        ids.add(cid)
        result.append(dict(id=cid, recordingId=rid, startMs=round(start), endMs=round(end),
                           manual=c.get('source') == 'manual', liked=c.get('reviewStatus') == 'kept',
                           title=str(c.get('title') or sources[rid]['title'])[:200], notes=str(c.get('notes', ''))[:2000],
                           reasons=c.get('reasons', []) if isinstance(c.get('reasons', []), list) else []))
    if not result:
        raise ValueError('No eligible candidates; add or scan moments in Clip Studio first')
    result.sort(key=lambda c: (not (c['manual'] or c['liked']), not c['liked'], c['recordingId'], c['startMs']))
    used = {c['recordingId'] for c in result}
    return dict(date=payload['date'], candidates=result, recordings=[r for r in sources.values() if r['id'] in used])


def validate_plan(plan, manifest):
    candidates = {c['id']: c for c in manifest['candidates']}
    reviewed = plan.get('reviews', [])
    reviewed_ids = [r.get('candidateId') for r in reviewed]
    priority = {c['id'] for c in candidates.values() if c['manual'] or c['liked']}
    if len(set(reviewed_ids)) != len(reviewed_ids) or any(cid not in candidates for cid in reviewed_ids) or not priority <= set(reviewed_ids):
        raise ValueError('Agent must review every manual and liked candidate')
    if any(not isinstance(r.get('reason'), str) or not r['reason'].strip() for r in reviewed):
        raise ValueError('Candidate reviews need explanations')
    clips = plan.get('clips', [])
    if not 1 <= len(clips) <= 24:
        raise ValueError('Edit must have 1–24 clips')
    total, intervals = 0, {}
    for clip in clips:
        c = candidates.get(clip.get('candidateId'))
        start, end = clip.get('startMs'), clip.get('endMs')
        if not c or not number(start) or not number(end) or not c['startMs'] <= start < end <= c['endMs'] or end-start < 500:
            raise ValueError('Agent cut exceeds its candidate boundary')
        if any(start < b and end > a for a, b in intervals.get(c['recordingId'], [])):
            raise ValueError('Edit repeats overlapping source material')
        intervals.setdefault(c['recordingId'], []).append((start, end))
        total += end-start
    if total > 135000:
        raise ValueError('Highlight must be at most 2:15')
    if priority and not any(c['candidateId'] in priority for c in clips):
        raise ValueError('Edit must include a manual or liked moment when available')
    if not isinstance(plan.get('title'), str) or not plan['title'].strip():
        raise ValueError('Missing film title')
    return total


def schema():
    def obj(props):
        return dict(type='object', properties=props, required=list(props), additionalProperties=False)
    text = dict(type='string')
    return obj(dict(title=text, summary=text,
                    reviews=dict(type='array', items=obj(dict(candidateId=text, reason=text))),
                    clips=dict(type='array', items=obj(dict(candidateId=text, startMs=dict(type='integer'), endMs=dict(type='integer'), reason=text)))))
