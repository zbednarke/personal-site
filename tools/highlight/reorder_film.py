"""Reorder exact saved segments of a finished film (including baked pitch trail).

Usage: python3 reorder_film.py VIDEO EDIT_JSON OUTPUT --order 9,11,10,12,13
Passage numbers are one-based indices into the original edit manifest.
"""
import argparse
import json
import subprocess
from pathlib import Path
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('video',type=Path);p.add_argument('edit',type=Path);p.add_argument('output',type=Path)
p.add_argument('--order',required=True)
a=p.parse_args()
e=json.loads(a.edit.read_text());indices=[int(x)-1 for x in a.order.split(',')]
assert indices and all(0<=i<len(e['segments']) for i in indices),'Invalid passage order'
segments=[e['segments'][i] for i in indices]
duration=float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','default=nw=1:nk=1',str(a.video)]))
filters=[]
for k,(i,s) in enumerate(zip(indices,segments)):
 end=duration if i==len(e['segments'])-1 else s['end']
 filters += [f"[0:v]trim=start={s['start']}:end={end},setpts=PTS-STARTPTS[v{k}]",f"[0:a]atrim=start={s['start']}:end={end},asetpts=PTS-STARTPTS[a{k}]"]
filters.append(''.join(f'[v{k}][a{k}]' for k in range(len(segments)))+f'concat=n={len(segments)}:v=1:a=1[v][a]')
a.output.parent.mkdir(parents=True,exist_ok=True)
subprocess.run(['ffmpeg','-v','error','-y','-i',str(a.video),'-filter_complex',';'.join(filters),'-map','[v]','-map','[a]','-c:v','libx264','-preset','fast','-crf','18','-fps_mode','vfr','-c:a','aac','-b:a','192k','-movflags','+faststart',str(a.output)],check=True)
a.output.with_suffix('.json').write_text(json.dumps({'sourceFilm':str(a.video),'sourceEdit':str(a.edit),'originalPassages':[i+1 for i in indices],'segments':segments},indent=2))
