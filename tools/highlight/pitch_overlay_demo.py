"""Render a 15-second pitch-roll study or overlay an existing complete film.

See docs/jazz-workflows.md for inputs, reproduction and sync validation.

Uses source-relative note timestamps, never a new microphone recording.
Requires Pillow and FFmpeg. No source files or published films are modified.
"""
import argparse,json,math,subprocess
from pathlib import Path
from PIL import Image,ImageDraw,ImageFont

parser=argparse.ArgumentParser()
parser.add_argument('job',type=Path)
parser.add_argument('output',type=Path)
parser.add_argument('--clip',type=int,default=3)
parser.add_argument('--full',action='store_true',help='Overlay the existing complete film, preserving its cuts and audio')
args=parser.parse_args()
clips=json.loads((args.job/'project.json').read_text())['clips']
analyses=json.loads((args.job/'analysis.json').read_text())
def probe(path):
 return json.loads(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration:stream=codec_type,width,height','-of','json',str(path)]))
segments=[];cursor=0
# Hosted jobs remove intermediate cuts after publication. Recover their starts
# from the existing film's keyframes near the saved edit boundaries; fail closed
# if a boundary is ambiguous, rather than silently drifting onto another take.
keyframes=None
if args.full and not (args.job/'cut-00.mp4').exists():
 packets=json.loads(subprocess.check_output(['ffprobe','-v','error','-select_streams','v:0','-show_packets','-show_entries','packet=pts_time,flags','-of','json',str(args.job/'highlight.mp4')]))['packets']
 keys=[float(p['pts_time']) for p in packets if 'K' in p['flags']]
 origin=keys[0];keyframes=[t-origin for t in keys]
for index,c in enumerate(clips):
 if keyframes is None:
  length=float(probe(args.job/f'cut-{index:02d}.mp4')['format']['duration'])
 else:
  matches=[t for t in keyframes if abs(t-cursor)<.08]
  assert len(matches)==1,f'Ambiguous cut boundary {index}: {matches}'
  cursor=matches[0]
  if segments:segments[-1]['end']=cursor
  length=(c['endMs']-c['startMs'])/1000
 ns=[n for n in analyses[c['recordingId']]['notes'] if n['endMs']>c['startMs'] and n['startMs']<c['endMs']]
 segments.append(dict(clip=c,start=cursor,end=cursor+length,notes=ns));cursor+=length
if args.full:
 source=args.job/'highlight.mp4';duration=float(probe(source)['format']['duration'])
else:
 source=args.job/f'cut-{args.clip:02d}.mp4';duration=min(15,segments[args.clip]['end']-segments[args.clip]['start'])
video=next(s for s in probe(source)['streams'] if s['codec_type']=='video')
width,height=(video['width'],video['height']) if args.full else (1280,720)
W,H,FPS=1280,174,30
fontroot=Path('/System/Library/Fonts')
regular=str(fontroot/'Supplemental/Arial Unicode.ttf');bold=str(fontroot/'Supplemental/Arial Bold.ttf')
small=ImageFont.truetype(regular,12);label=ImageFont.truetype(bold,11);large=ImageFont.truetype(regular,37)
shift=2

names=['C','C♯','D','E♭','E','F','F♯','G','A♭','A','B♭','B']
def note_name(m):return names[m%12]+str(m//12-1)
left,right,now=220,1232,530
scale=145
args.output.parent.mkdir(parents=True,exist_ok=True)
command=['ffmpeg','-v','error','-y','-i',str(source),'-f','rawvideo','-pixel_format','rgba','-video_size',f'{W}x{H}','-framerate',str(FPS),'-i','pipe:0','-filter_complex',f'[0:v]scale={width}:{height}[base];[1:v]scale={width}:{round(H*width/W)}[roll];[base][roll]overlay=0:H-h:shortest=1[v]','-map','[v]','-map','0:a:0','-t',str(duration),'-c:v','libx264','-preset','fast','-crf','19','-pix_fmt','yuv420p','-c:a','copy','-fps_mode','passthrough','-enc_time_base','-1','-movflags','+faststart',str(args.output)]
proc=subprocess.Popen(command,stdin=subprocess.PIPE)
try:
 for f in range(math.ceil(duration*FPS)):
  elapsed=f/FPS
  segment=next((s for s in segments if s['start']<=elapsed<s['end']),segments[-1]) if args.full else segments[args.clip]
  clip=segment['clip'];notes=segment['notes']
  t=clip['startMs']+(elapsed-segment['start'] if args.full else elapsed)*1000
  lo=min((n['midi'] for n in notes),default=48)+shift-2;hi=max((n['midi'] for n in notes),default=72)+shift+2
  if hi-lo<12:
   middle=(hi+lo)//2;lo=middle-6;hi=middle+6
  im=Image.new('RGBA',(W,H));d=ImageDraw.Draw(im)
  # Soft fade above the panel; leave the player and instrument visible.
  for y in range(H):d.line((0,y,W,y),fill=(12,23,24,min(220,int(y/24*210))))
  d.text((32,30),'P I T C H   T R A I L',font=label,fill=(172,205,192,255))
  active=next((n for n in notes if n['startMs']<=t<n['endMs']),None)
  d.text((32,52),note_name(active['midi']+shift) if active else '—',font=large,fill=(242,241,218,255))
  d.text((32,111),'Written B♭ trumpet',font=small,fill=(181,197,190,255))
  d.text((32,131),'Estimated from audio',font=small,fill=(143,161,155,255))
  def py(m):return 140-(m-lo)/(hi-lo)*96
  for m in range(lo,hi+1):
   y=py(m)
   d.line((left,y,right,y),fill=(45,66,60,230) if m%12 else (72,91,79,240))
   if m%12==0:d.text((left-29,y-7),note_name(m),font=small,fill=(181,197,190,220))
  d.text((now+8,23),'NOW',font=label,fill=(242,241,218,255))
  for n in notes:
   x1=now+(n['startMs']-t)/1000*scale;x2=now+(n['endMs']-t)/1000*scale
   if x2<left or x1>right:continue
   x1=max(left,x1);x2=min(right,x2);y=py(n['midi']+shift)
   color=(241,237,192,255) if n is active else (156,207,183,225) if n['endMs']<=t else (107,141,127,235)
   if n is active:d.rounded_rectangle((x1,y-7,x2,y+7),radius=5,fill=(73,91,70,225))
   d.rounded_rectangle((x1,y-3,x2,y+3),radius=3,fill=color)
   if x2-x1>46:d.text((x1+5,y-19),note_name(n['midi']+shift),font=small,fill=color)
  d.line((now,42,now,148),fill=(247,242,208,230),width=2)
  d.ellipse((now-3,146,now+3,152),fill=(247,242,208,255))
  proc.stdin.write(im.tobytes())
finally:proc.stdin.close()
assert proc.wait()==0,'FFmpeg render failed'
if args.full:
 args.output.with_suffix('.edit.json').write_text(json.dumps(dict(sourceJob=args.job.name,clips=clips,segments=[{k:v for k,v in s.items() if k!='notes'} for s in segments]),indent=2))
print(args.output)
