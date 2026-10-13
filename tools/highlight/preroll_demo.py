"""Four-second whole-day atlas preview with the existing film's opening audio.

Requires Pillow, FFmpeg and macOS Arial fonts. Does not modify the source film.
Example: python3 tools/highlight/preroll_demo.py JOB OUTPUT --date 2026-09-30
"""
import argparse
import json
import math
import subprocess
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('job', type=Path)
p.add_argument('output', type=Path)
p.add_argument('--date', required=True)
p.add_argument('--video', type=Path, help='Optional existing pitch-roll film for the transition')
p.add_argument('--start', type=float, default=0, help='Start time in the existing film, seconds')
p.add_argument('--first-clip', type=int, default=1, help='One-based opening clip for atlas emphasis')
p.add_argument('--full', action='store_true', help='Keep the rest of the film after the four-second intro')
a = p.parse_args()
data = json.loads((a.job / 'analysis.json').read_text())
clips = json.loads((a.job / 'project.json').read_text())['clips']
clips = clips[a.first_clip-1:]
if not clips:
    raise SystemExit('Opening clip is outside the saved edit')
items = list(data.items())
W, H, FPS, SECONDS = 1920, 1080, 30, 4
fonts = Path('/System/Library/Fonts/Supplemental')
def font(size, bold=False):
    return ImageFont.truetype(str(fonts / ('Arial Bold.ttf' if bold else 'Arial Unicode.ttf')), size)
heading, label, small = font(62, True), font(23, True), font(19)
cream, muted = (238,239,221), (151,177,169)
def color(v):
    return (165,211,190) if 'long tone' in v['section'].lower() else (229,185,121) if 'study' in v['section'].lower() else (180,169,226)
def ease(x):
    x = max(0, min(1, x))
    return x*x*(3-2*x)
cols = 3
rows = math.ceil(len(items)/cols)
left, top, gap = 88, 230, 18
cw = (W-2*left-(cols-1)*gap)/cols
ch = (H-top-95-(rows-1)*gap)/rows
source = a.video or a.job/'highlight.mp4'
if a.full:
    duration = float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','default=nw=1:nk=1',str(source)]))
    SECONDS = duration-a.start
if a.start < 0 or SECONDS <= 0:
    raise SystemExit('Invalid film start or duration')
a.output.parent.mkdir(parents=True, exist_ok=True)
cmd = ['ffmpeg','-v','error','-y','-ss',str(a.start),'-i',str(source),'-f','rawvideo','-pixel_format','rgba','-video_size',f'{W}x{H}','-framerate',str(FPS),'-i','pipe:0','-filter_complex',f'[0:v]scale={W}:{H},fps={FPS},setpts=PTS-STARTPTS[base];[base][1:v]overlay=0:0:shortest=1[v]','-map','[v]','-map','0:a:0','-t',str(SECONDS),'-c:v','libx264','-preset','fast','-crf','18','-pix_fmt','yuv420p','-c:a','aac','-b:a','192k','-movflags','+faststart',str(a.output)]
proc = subprocess.Popen(cmd, stdin=subprocess.PIPE)
try:
    for frame in range(math.ceil(FPS*SECONDS)):
        t = frame/FPS
        im = Image.new('RGBA',(W,H),(15,25,25,255))
        d = ImageDraw.Draw(im)
        d.text((left,53),'THE SHAPE OF A PRACTICE DAY',font=small,fill=muted)
        d.text((left,90),'Every take, one day.',font=heading,fill=cream)
        minutes = sum(v['durationMs'] for _,v in items)/60000
        d.text((left,175),f'{a.date}   /   {len(items)} takes   /   {minutes:.0f} minutes recorded',font=label,fill=muted)
        reveal = ease((t-.25)/1.55)
        for i,(rid,v) in enumerate(items):
            x=left+(i%cols)*(cw+gap); y=top+(i//cols)*(ch+gap)
            c=color(v)
            active=rid==clips[0]['recordingId']
            emphasis=ease((t-2.6)/.45) if active else 0
            border=tuple(int(48+(z-48)*emphasis) for z in c)
            d.rounded_rectangle((x,y,x+cw,y+ch),radius=14,fill=(24,37,36),outline=border,width=2)
            title='Long tones' if 'long tone' in v['section'].lower() else 'ii–V–I · Study 1' if 'training' in v['section'].lower() else v['section']
            d.text((x+18,y+13),f'{i+1:02d}  {title}',font=label,fill=c)
            dur=v['durationMs']/1000
            stamp=f'{int(dur//60)}:{int(dur%60):02d}'
            d.text((x+cw-70,y+16),stamp,font=small,fill=muted)
            notes=v['notes']; lo=min((n['midi'] for n in notes),default=48)-2; hi=max((n['midi'] for n in notes),default=72)+2
            for n in notes:
                start=n['startMs']/v['durationMs']; end=n['endMs']/v['durationMs']
                if start>reveal: continue
                nx=x+18+start*(cw-36); ex=x+18+min(end,reveal)*(cw-36)
                ny=y+ch-19-(n['midi']-lo)/(hi-lo)*(ch-65)
                d.line((nx,ny,max(nx+1,ex),ny),fill=c,width=3)
            if not notes:
                d.text((x+18,y+ch-40),'No sustained pitch detected',font=small,fill=muted)
            if emphasis:
                sx=x+18+clips[0]['startMs']/v['durationMs']*(cw-36)
                ex=x+18+clips[0]['endMs']/v['durationMs']*(cw-36)
                d.rectangle((sx,y+49,max(sx+4,ex),y+ch-12),outline=cream,width=2)
        d.text((left,H-56),'LONG TONES',font=small,fill=(165,211,190))
        d.text((left+190,H-56),'ii–V–I STUDIES',font=small,fill=(229,185,121))
        d.text((W-435,H-56),'Pitch estimated from your recordings',font=small,fill=muted)
        # Reveal the matching video timestamp; audio runs continuously underneath.
        alpha=round(255*(1-ease((t-3.45)/.5)))
        im.putalpha(alpha)
        proc.stdin.write(im.tobytes())
finally:
    proc.stdin.close()
if proc.wait():
    raise SystemExit('FFmpeg render failed')
a.output.with_suffix('.json').write_text(json.dumps({'date':a.date,'sourceJob':a.job.name,'sourceFilm':str(source),'durationSeconds':SECONDS,'audio':'continuous source film audio from startSeconds','startSeconds':a.start,'retainedClips':clips,'takes':list(data),'transitionStartSeconds':3.45},indent=2))
print(a.output)
