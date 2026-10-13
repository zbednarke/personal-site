"""Offline monophonic note evidence and phrase selection; never a musical grade.

Decode sequentially with FFmpeg. NumPy batches keep DSP working memory bounded.
All timestamps use the decoded asset's clock (embedded video audio for video).
"""
import hashlib
import json
import math
import subprocess
from pathlib import Path

import numpy as np

VERSION = 'note-map-v1'
RATE, WINDOW, HOP = 16000, 1024, 640
NAMES = ['C', 'C♯', 'D', 'E♭', 'E', 'F', 'F♯', 'G', 'A♭', 'A', 'B♭', 'B']


def observations(samples, offset=0):
    """YIN-style normalized difference, FFT accelerated, independent of targets."""
    if len(samples) < WINDOW:
        return []
    frames = np.lib.stride_tricks.sliding_window_view(samples, WINDOW)[::HOP].copy()
    levels = np.sqrt(np.mean(frames ** 2, axis=1))
    clipped = np.mean(np.abs(frames) >= .985, axis=1)
    frames -= frames.mean(axis=1, keepdims=True)
    spectrum = np.fft.rfft(frames, n=2048, axis=1)
    corr = np.fft.irfft(spectrum * spectrum.conj(), n=2048, axis=1)[:, :248]
    sums = np.concatenate([np.zeros((len(frames), 1)), np.cumsum(frames ** 2, axis=1)], axis=1)
    lags = np.arange(1, 248)
    difference = np.maximum(0, sums[:, WINDOW-lags] + sums[:, -1:] - sums[:, lags] - 2*corr[:, 1:]) / (WINDOW-lags)
    norm = difference * lags / np.maximum(np.cumsum(difference, axis=1), 1e-12)
    out = []
    for i, row in enumerate(norm):
        lag = next((k for k in range(11, 245) if row[k-1] < .12), None)
        hz = midi = None
        confidence = 0.
        if levels[i] >= .008 and lag is not None:
            while lag < 246 and row[lag] < row[lag-1]:
                lag += 1
            a, b, c = row[lag-2:lag+1]
            denominator = 2*(2*b-a-c)
            refined = lag + ((c-a)/denominator if denominator else 0)
            hz = RATE/refined
            confidence = float(1-b)
            if 65 <= hz <= 1400:
                midi = 69+12*math.log2(hz/440)
            else:
                hz = None
        out.append(dict(timeMs=round((offset+i*HOP)/RATE*1000), rms=round(float(levels[i]), 5),
                        clipped=round(float(clipped[i]), 5), midi=round(midi, 3) if midi is not None else None,
                        confidence=round(confidence, 3)))
    return out


def summarize(frames, start=0, end=None):
    f = [x for x in frames if start <= x['timeMs'] < (end if end is not None else math.inf)]
    voiced = [x for x in f if x['midi'] is not None]
    audible = [x for x in f if x['rms'] >= .008]
    histogram = {}
    for x in voiced:
        note = round(x['midi']) % 12
        histogram[NAMES[note]] = round(histogram.get(NAMES[note], 0) + HOP/RATE, 2)
    return dict(voicedFraction=round(len(voiced)/max(1, len(f)), 3),
                readableFraction=round(len(voiced)/max(1, len(audible)), 3),
                clippingFraction=round(sum(x['clipped'] for x in f)/max(1, len(f)), 5),
                noteSeconds=histogram, pitchConvention='concert pitch; octave estimates can be ambiguous')


def note_events(frames):
    events, run = [], []
    def emit():
        if len(run) < 3:
            return
        pitches = [f['midi'] for f in run]
        midi = round(float(np.median(pitches)))
        events.append(dict(startMs=run[0]['timeMs'], endMs=run[-1]['timeMs']+40,
                           midi=midi, note=NAMES[midi % 12]+str(midi//12-1),
                           cents=round((float(np.median(pitches))-midi)*100),
                           spreadCents=round(float(np.percentile(pitches, 90)-np.percentile(pitches, 10))*100),
                           confidence=round(float(np.mean([f['confidence'] for f in run])), 3)))
    for frame in frames:
        if frame['midi'] is None or (run and round(frame['midi']) != round(run[-1]['midi'])):
            emit(); run = []
        if frame['midi'] is not None:
            run.append(frame)
    emit()
    return events


def analyze(path, cancelled=lambda: False):
    frames, pending, offset = [], np.empty(0, dtype=np.float32), 0
    # Never capture a whole decoded recording in a subprocess output buffer.
    with subprocess.Popen(['ffmpeg', '-v', 'error', '-i', str(path), '-vn', '-ac', '1', '-ar', str(RATE),
                           '-f', 'f32le', '-'], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as proc:
        try:
            while True:
                if cancelled():
                    raise InterruptedError('Cancelled')
                raw = proc.stdout.read(4*HOP*128)
                if not raw:
                    break
                pending = np.concatenate([pending, np.frombuffer(raw, dtype='<f4')])
                batch = observations(pending, offset)
                frames.extend(batch)
                used = len(batch)*HOP
                pending = pending[used:]
                offset += used
            if proc.wait() != 0:
                raise RuntimeError('Audio decoding failed')
        finally:
            if proc.poll() is None:
                proc.kill(); proc.wait()
    if not frames:
        raise ValueError('No analyzable audio in recording')
    return dict(version=VERSION, hopMs=40, durationMs=round((offset+len(pending))/RATE*1000),
                summary=summarize(frames), notes=note_events(frames), frames=frames,
                limitations='Monophonic pitch estimates, not transcription or a correctness grade. Speech, chords, noise and distorted capture can be ambiguous.')


def cached_analysis(path, cache, cancelled=lambda: False):
    digest = hashlib.sha256()
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1024*1024), b''):
            if cancelled(): raise InterruptedError('Cancelled')
            digest.update(block)
    key = VERSION+'-'+digest.hexdigest()
    cache = Path(cache); cache.mkdir(parents=True, exist_ok=True)
    target = cache/(key+'.json')
    if target.exists():
        return json.loads(target.read_text())
    result = analyze(path, cancelled)
    result['sourceSha256'] = digest.hexdigest()
    temp = target.with_suffix('.tmp')
    temp.write_text(json.dumps(result, separators=(',', ':'))); temp.replace(target)
    return result


def candidates(recording, analysis, excluded=()):
    """Find complete pitched phrases, with bounded excerpts for very long holds."""
    notes = analysis['notes']; groups = []
    for note in notes:
        if groups and note['startMs']-groups[-1][-1]['endMs'] < 700:
            groups[-1].append(note)
        else:
            groups.append([note])
    found = []
    limit = min(recording['durationMs'], analysis['durationMs'])
    for group in groups:
        start, finish = max(0, group[0]['startMs']-180), min(limit, group[-1]['endMs']+220)
        while finish-start >= 4000:
            end = min(finish, start+18000)
            # Prefer the end of a note / breath to a rigid cut.
            ends = [n['endMs']+180 for n in group if start+8000 <= n['endMs']+180 <= end]
            if end < finish and ends: end = max(ends)
            if any(start < b and end > a for a,b in excluded):
                start = end; continue
            quality = summarize(analysis['frames'], start, end)
            if quality['voicedFraction'] >= .65 and quality['readableFraction'] >= .8 and quality['clippingFraction'] < .01:
                selected_notes = [n for n in group if n['startMs'] >= start and n['endMs'] <= end]
                found.append(dict(id=f"map-{recording['id']}-{start}", recordingId=recording['id'],
                    startMs=start, endMs=end, manual=False, liked=False, generated=True,
                    title=recording['title'], section=recording.get('section', recording['title']),
                    notes='', reasons=['Reliable pitched activity', 'Phrase-aware boundaries'],
                    audioEvidence=quality, detectedNotes=selected_notes,
                    score=round(quality['voicedFraction']*.6+quality['readableFraction']*.4, 3)))
            start = end
    return sorted(found, key=lambda c: -c['score'])[:30]


def select_plan(manifest):
    """Deterministic, auditable balanced draft; no target-note-dependent selection."""
    pool = list(manifest['candidates']); chosen=[]; used={}; sections={}; total=0
    target=manifest.get('targetSeconds', 120)*1000
    passage_limit=min(18000,max(8000,target//max(1,len({c['recordingId'] for c in pool}))))
    while pool and total < target-500:
        def rank(c):
            priority = 4 if c['manual'] or c['liked'] else 0
            return priority+c.get('score', .5)+1/(1+used.get(c['recordingId'],0))+1/(1+sections.get(c.get('section',c['title']),0))
        pool.sort(key=rank, reverse=True)
        c=pool.pop(0)
        if any(c['recordingId']==x['recordingId'] and c['startMs']<x['endMs'] and c['endMs']>x['startMs'] for x in chosen): continue
        remaining=target-total
        end=min(c['endMs'],c['startMs']+remaining,c['startMs']+(remaining if c['manual'] or c['liked'] else passage_limit))
        if end-c['startMs']<3000:continue
        # A final short trim ends at a known note boundary where possible.
        if end<c['endMs']:
            ends=[n['endMs']+160 for n in c.get('detectedNotes',[]) if c['startMs']+3000 <= n['endMs']+160 <= end]
            if ends:end=max(ends)
        selected={**c,'endMs':end};chosen.append(selected);total+=end-c['startMs']
        used[c['recordingId']]=used.get(c['recordingId'],0)+1
        section=c.get('section',c['title']);sections[section]=sections.get(section,0)+1
        if len(chosen)>=24:break
    if not chosen:raise ValueError('No reliable passages found; add a manual selection rather than guessing.')
    order={r['id']:i for i,r in enumerate(manifest['recordings'])}
    chosen.sort(key=lambda c:(order[c['recordingId']],c['startMs']))
    return dict(title=f"{manifest['date']} · Practice in two minutes" if target==120000 else f"{manifest['date']} · Practice highlights",
                summary=f"{len(chosen)} passages from {len(used)} takes across {len(sections)} sections. Selected for readable pitch and coverage, not a musical grade.",
                reviews=[dict(candidateId=c['id'],reason='User-selected moment considered; selected when compatible with duration and overlap constraints.') for c in manifest['candidates'] if c['manual'] or c['liked']],
                clips=[dict(candidateId=c['id'],startMs=c['startMs'],endMs=c['endMs'],reason='User-selected moment' if c['manual'] or c['liked'] else 'Clear pitched phrase; balances coverage across today’s takes') for c in chosen])
