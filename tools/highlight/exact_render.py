"""Exact-edit renderer: no selection, stable clip IDs, frame-based cut manifest."""
from copy import deepcopy
import hashlib
import json
import shutil
import sys
from pathlib import Path
from worker import write_json
from publish import checksum
from note_map import render_note_map

def requires_composition(edit, clips):
    ap=edit['appearance']
    return ap['intro'] or ap['pitchTrail'] or any(c.get('graphics',{}).get('chapter') or c.get('graphics',{}).get('captions') for c in clips)


def render_exact(worker, edit, manifest, folder, preview=False):
    sources,_=worker.prepare(manifest,folder)
    maps=json.loads((folder/'analysis.json').read_text())
    source_film=edit.get('sourceFilmId')
    if source_film:
        original=worker.films/(source_film+'.analysis.json')
        if original.exists(): maps={**json.loads(original.read_text()),**maps}
    write_json(folder/'analysis.json',maps)
    (folder/'note-map.html').write_text(render_note_map(maps,edit['date']))
    # Composition needs note events, not every 40 ms observation. Avoid keeping
    # the full-day frame arrays alive in both the worker and graphics subprocess.
    write_json(folder/'graphics-analysis.json',{rid:{k:v for k,v in m.items() if k!='frames'} for rid,m in maps.items()})
    del maps
    clips=deepcopy(edit['clips'])
    if preview:
        remaining=6000;short=[]
        for c in clips:
            length=min(remaining,c['endMs']-c['startMs'])
            if length<=0:break
            c['endMs']=c['startMs']+length;short.append(c);remaining-=length
        clips=short
    segments=[];frames=0
    needs_graphics=requires_composition(edit,clips)
    worker.status(worker.jobs[worker.active],'rendering','Rendering your saved passages')
    for i,c in enumerate(clips):
        worker.status(worker.jobs[worker.active],'rendering',f'Rendering passage {i+1} of {len(clips)}')
        source=sources[c['recordingId']];n=max(1,round((c['endMs']-c['startMs'])*30/1000));duration=n/30
        args=['ffmpeg','-v','error','-y']
        if source['video']:
            args+=['-ss',str(c['startMs']/1000),'-t',str(duration),'-i',str(source['video']),'-map','0:v:0','-map','0:a:0']
        else:
            from PIL import Image, ImageDraw
            from composition import font
            slate=Image.new('RGB',(1920,1080),(25,28,25));d=ImageDraw.Draw(slate)
            d.text((160,420),'Audio recording',font=font(64,True),fill=(228,237,218));d.text((160,530),edit['date'],font=font(42),fill=(180,201,180))
            slate.save(folder/f'slate-{i}.png')
            args+=['-loop','1','-framerate','30','-t',str(duration),'-i',str(folder/f'slate-{i}.png'),'-ss',str(c['startMs']/1000),'-t',str(duration),'-i',str(source['audio']),'-map','0:v:0','-map','1:a:0']
        size='854:480' if preview else '1920:1080'
        w,h=size.split(':')
        vf=f'scale={size}:force_original_aspect_ratio=decrease,pad={size}:(ow-iw)/2:(oh-ih)/2,fps=30,tpad=stop_mode=clone:stop_duration=1,trim=end_frame={n},setpts=N/(30*TB),format=yuv420p'
        # Graphics require a final encode; keep intermediate encoding inexpensive.
        preset='ultrafast' if needs_graphics else 'veryfast'
        args+=['-vf',vf,'-af',f'aresample=48000,apad,atrim=duration={duration},asetpts=N/SR/TB','-t',str(duration),'-c:v','libx264','-preset',preset,'-crf','18','-threads','2','-c:a','pcm_s16le','-ac','1',str(folder/f'cut-{i:02d}.mov')]
        worker.command(args,folder)
        segments.append(dict(start=frames/30,end=(frames+n)/30,startFrame=frames,endFrame=frames+n,clip=c));frames+=n
    (folder/'concat.txt').write_text('\n'.join(f"file 'cut-{i:02d}.mov'\nduration {(segment['endFrame']-segment['startFrame'])/30:.9f}" for i,segment in enumerate(segments)))
    worker.command(['ffmpeg','-v','error','-y','-f','concat','-safe','1','-i','concat.txt','-c:v','copy','-c:a','aac','-b:a','192k','-movflags','+faststart','base.mp4'],folder)
    recipe=dict(schemaVersion=1,rendererVersion='exact-film-v1',date=edit['date'],owner=edit['owner'],editId=edit['id'],revision=edit['revision'],sourceFilmId=edit['sourceFilmId'],edit={k:edit[k] for k in ('title','clips','appearance')},sources=edit['sources'],segments=segments,durationSeconds=frames/30,appearance=edit['appearance'],sourceHashes={rid:checksum(s['audio']) for rid,s in sources.items()},analysisSha256=checksum(folder/'analysis.json'))
    recipe['rendererSha256']=hashlib.sha256(Path(__file__).read_bytes()+Path(__file__).with_name('composition.py').read_bytes()).hexdigest()
    recipe['ffmpegVersion']=worker.command(['ffmpeg','-version'],folder).decode().splitlines()[0]
    write_json(folder/'edit.json',recipe)
    ap=edit['appearance']
    if needs_graphics:
        worker.status(worker.jobs[worker.active],'rendering','Adding day overview and pitch trail')
        worker.command([sys.executable,str(Path(__file__).with_name('composition.py')),'base.mp4','highlight.mp4','edit.json','graphics-analysis.json'],folder,timeout=3600)
    else:shutil.copyfile(folder/'base.mp4',folder/'highlight.mp4')
    worker.status(worker.jobs[worker.active],'rendering','Checking video and audio playback')
    worker.command(['ffmpeg','-v','error','-i','highlight.mp4','-f','null','-'],folder)
    probe=json.loads(worker.command(['ffprobe','-v','error','-show_entries','format=duration:stream=codec_type,nb_frames','-of','json','highlight.mp4'],folder))
    actual=float(probe['format']['duration'])
    video=next((s for s in probe['streams'] if s['codec_type']=='video'),{})
    if abs(actual-frames/30)>.1 or int(video.get('nb_frames',0))!=frames or not any(s['codec_type']=='audio' for s in probe['streams']):raise RuntimeError(f'Rendered streams do not match {frames} frames: {probe}')
    recipe['outputSha256']=checksum(folder/'highlight.mp4');write_json(folder/'edit.json',recipe)
    project=dict(version=1,date=edit['date'],title=edit['title'],clips=[{**c,'candidateId':c['id'],'generated':True,'liked':False,'notes':'Saved film edit'} for c in clips])
    write_json(folder/'project.json',project)
    return project,round(actual*1000)
