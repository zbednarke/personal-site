"""Portable, deterministic film graphics; consumes saved source-clock segments.

Pillow and FFmpeg required. Uses system DejaVu or macOS Arial fonts, or
JAZZ_FONT_DIR containing DejaVuSans.ttf and DejaVuSans-Bold.ttf.
"""
import argparse
import json
import math
import os
from pathlib import Path
import subprocess
from PIL import Image, ImageDraw, ImageFont

W,H,FPS=1280,720,30
CREAM=(238,239,221); MUTED=(151,177,169)

def ease(x):
    x=max(0,min(1,x)); return x*x*(3-2*x)

def font(size,bold=False):
    root=os.environ.get('JAZZ_FONT_DIR','/usr/share/fonts/truetype/dejavu')
    candidates=[Path(root)/('DejaVuSans-Bold.ttf' if bold else 'DejaVuSans.ttf'),Path('/System/Library/Fonts/Supplemental')/('Arial Bold.ttf' if bold else 'Arial Unicode.ttf')]
    for p in candidates:
        if p.exists(): return ImageFont.truetype(str(p),size)
    raise RuntimeError('Install DejaVu fonts or configure JAZZ_FONT_DIR')

def section_color(v):
    s=v.get('section',v.get('title','')).lower()
    return (165,211,190) if 'long tone' in s else (229,185,121) if 'study' in s or 'training' in s else (180,169,226)

def note_name(m):
    return ['C','C♯','D','E♭','E','F','F♯','G','A♭','A','B♭','B'][m%12]+str(m//12-1)

class Graphics:
    def __init__(self,maps,segments,date,appearance):
        self.maps,self.segments,self.date,self.ap=maps,segments,date,appearance
        self.small,self.label,self.heading=font(13),font(16,True),font(40,True)
        self.large,self.card,self.card_sub,self.caption=font(34),font(54,True),font(22),font(23,True)
        self.blank=Image.new('RGBA',(W,H))
        # Render complete day overview once; reveal masks are cheap per-frame.
        self.atlas=Image.new('RGBA',(W,H),(15,25,25,255));d=ImageDraw.Draw(self.atlas)
        d.text((55,30),'THE SHAPE OF A PRACTICE DAY',font=self.small,fill=MUTED)
        d.text((55,55),'Every take, one day.',font=self.heading,fill=CREAM)
        d.text((55,113),f'{date}   /   {len(maps)} takes   /   {sum(v["durationMs"] for v in maps.values())/60000:.0f} minutes recorded',font=self.label,fill=MUTED)
        items=list(maps.items())
        # Large days group into section strips; never shrink labels indefinitely.
        if len(items)>18:
            groups={}
            for rid,v in items:
                key=v.get('section',v.get('title','Practice'));g=groups.setdefault(key,dict(title=key,section=key,durationMs=0,notes=[]))
                offset=g['durationMs'];g['notes'] += [{**n,'startMs':n['startMs']+offset,'endMs':n['endMs']+offset} for n in v.get('notes',[])];g['durationMs']+=v['durationMs']
            grouped=list(groups.items())
            if len(grouped)>18:
                other=dict(title='Other sections',section='Other sections',durationMs=0,notes=[])
                for _,v in grouped[17:]:
                    offset=other['durationMs'];other['notes'] += [{**n,'startMs':n['startMs']+offset,'endMs':n['endMs']+offset} for n in v['notes']];other['durationMs']+=v['durationMs']
                grouped=grouped[:17]+[('other-sections',other)]
            items=grouped
            d.text((800,113),'Grouped by practice section',font=self.small,fill=MUTED)
        cols=3 if len(items)>6 else 2; rows=max(1,math.ceil(len(items)/cols)); gap=12; cw=(W-110-gap*(cols-1))/cols;ch=(H-208-gap*(rows-1))/rows
        self.regions=[]
        for i,(rid,v) in enumerate(items):
            x=55+(i%cols)*(cw+gap);y=157+(i//cols)*(ch+gap);c=section_color(v)
            d.rounded_rectangle((x,y,x+cw,y+ch),radius=9,fill=(24,37,36),outline=(49,65,61))
            title=v.get('title','Practice').replace('Study 1 · ii–V–I training','ii–V–I · Study 1')
            while d.textlength(title,font=self.label)>cw-98: title=title[:-2]+'…'
            d.text((x+12,y+9),f'{i+1:02d}  {title}',font=self.label,fill=c)
            dur=v['durationMs']/1000;d.text((x+cw-49,y+11),f'{int(dur//60)}:{int(dur%60):02d}',font=self.small,fill=MUTED)
            notes=v.get('notes',[]);lo=min((n['midi'] for n in notes),default=48)-2;hi=max((n['midi'] for n in notes),default=72)+2
            for n in notes:
                nx=x+12+n['startMs']/max(1,v['durationMs'])*(cw-24);ex=x+12+n['endMs']/max(1,v['durationMs'])*(cw-24)
                ny=y+ch-12-(n['midi']-lo)/(hi-lo)*max(5,ch-44)
                d.line((nx,ny,max(nx+1,ex),ny),fill=c,width=2)
            if not notes:d.text((x+12,y+ch-24),'Pitch unavailable',font=self.small,fill=MUTED)
            self.regions.append((x,y,cw,ch,rid,v))
        d.text((55,H-31),'LONG TONES',font=self.small,fill=(165,211,190));d.text((185,H-31),'ii–V–I STUDIES',font=self.small,fill=(229,185,121));d.text((W-260,H-31),'Pitch estimated from recordings',font=self.small,fill=MUTED)

    def frame(self,t):
        im=self.blank.copy();d=ImageDraw.Draw(im)
        seg=next((s for s in self.segments if s['start']<=t<s['end']),self.segments[-1]);c=seg['clip'];source_t=c['startMs']+(t-seg['start'])*1000
        graphics=c.get('graphics',{})
        pitch_trail=self.ap['pitchTrail'] and graphics.get('pitchTrail',True)
        if pitch_trail:
            top=H-174
            for y in range(174):d.line((0,top+y,W,top+y),fill=(12,23,24,min(220,int(y/24*210))))
            notes=self.maps.get(c['recordingId'],{}).get('notes',[])
            notes=[n for n in notes if n['endMs']>c['startMs'] and n['startMs']<c['endMs']]
            shift=2 if self.ap['pitchConvention']=='written-bb' else 0
            lo=min((n['midi'] for n in notes),default=48)+shift-2;hi=max((n['midi'] for n in notes),default=72)+shift+2
            if hi-lo<12:mid=(hi+lo)//2;lo,hi=mid-6,mid+6
            py=lambda m:top+140-(m-lo)/(hi-lo)*96
            active=next((n for n in notes if n['startMs']<=source_t<n['endMs']),None)
            d.text((32,top+30),'P I T C H   T R A I L',font=self.small,fill=MUTED)
            d.text((32,top+53),note_name(active['midi']+shift) if active else '—',font=self.large,fill=CREAM)
            d.text((32,top+112),'Written B♭ trumpet' if shift else 'Concert pitch',font=self.small,fill=MUTED)
            d.text((32,top+135),'Estimated from audio' if notes else 'Pitch unavailable',font=self.small,fill=MUTED)
            for m in range(lo,hi+1):d.line((220,py(m),1232,py(m)),fill=(50,70,63,220),width=1)
            for n in notes:
                x1=530+(n['startMs']-source_t)/1000*145;x2=530+(n['endMs']-source_t)/1000*145
                if x2<220 or x1>1232:continue
                x1=max(220,x1);x2=min(1232,x2);y=py(n['midi']+shift);color=(*CREAM,255) if n is active else (156,207,183,230)
                d.rounded_rectangle((x1,y-3,max(x1+1,x2),y+3),radius=2,fill=color)
                if x2-x1>46:d.text((x1+4,y-20),note_name(n['midi']+shift),font=self.small,fill=color)
            d.line((530,top+42,530,top+148),fill=(*CREAM,255),width=2);d.text((540,top+22),'NOW',font=self.small,fill=CREAM)
        chapter=graphics.get('chapter')
        local=t-seg['start']
        if chapter and local<3:
            alpha=round(255*ease(local/.3)*(1-ease((local-2.35)/.5)))
            if alpha:
                title=chapter.upper(); subtitle=graphics.get('subtitle')
                tw=d.textbbox((0,0),title,font=self.card)[2]
                x=64;y=72
                d.rounded_rectangle((x-18,y-14,x+tw+22,y+70+(42 if subtitle else 0)),radius=12,fill=(10,19,20,min(210,alpha)))
                d.text((x,y),title,font=self.card,fill=(*CREAM,alpha))
                if subtitle:d.text((x+2,y+66),subtitle,font=self.card_sub,fill=(*MUTED,alpha))
        caption=next((v for v in graphics.get('captions',[]) if v['startMs']<=source_t<v['endMs']),None)
        if caption:
            text=caption['text']; box=d.textbbox((0,0),text,font=self.caption);tw=box[2]-box[0]
            x=(W-tw)/2;y=H-(224 if pitch_trail else 72)
            d.rounded_rectangle((x-18,y-10,x+tw+18,y+38),radius=10,fill=(8,14,15,225))
            d.text((x,y),text,font=self.caption,fill=CREAM)
        length=self.ap['introSeconds']
        if self.ap['intro'] and t<length:
            atlas=self.atlas.copy();ad=ImageDraw.Draw(atlas);reveal=ease((t-.2)/max(.5,length*.4))
            for x,y,cw,ch,rid,v in self.regions:
                if reveal<1:ad.rectangle((x+12+reveal*(cw-24),y+33,x+cw-12,y+ch-10),fill=(24,37,36))
                first=self.segments[0]['clip']
                if rid==first['recordingId'] and t>length-1.4:
                    ad.rounded_rectangle((x,y,x+cw,y+ch),radius=9,outline=(*CREAM,255),width=2)
            atlas.putalpha(round(255*(1-ease((t-(length-.55))/.5))))
            im=Image.alpha_composite(im,atlas)
        return im

def render(source,output,recipe,analysis):
    g=Graphics(analysis,recipe['segments'],recipe['date'],recipe['appearance']); duration=recipe['durationSeconds']
    cmd=['ffmpeg','-v','error','-y','-i',str(source),'-f','rawvideo','-pixel_format','rgba','-video_size',f'{W}x{H}','-framerate',str(FPS),'-i','pipe:0','-filter_complex_threads','1','-filter_complex','[1:v][0:v]scale2ref[overlay][base];[base][overlay]overlay=0:0:eof_action=repeat[v]','-map','[v]','-map','0:a:0','-t',str(duration),'-c:v','libx264','-preset','veryfast','-crf','18','-threads','2','-pix_fmt','yuv420p','-c:a','copy','-fps_mode','passthrough','-enc_time_base','-1','-movflags','+faststart',str(output)]
    # The parent worker owns cancellation. Terminating this process closes the pipe
    # and stops FFmpeg; suppress stderr piping so a full buffer cannot deadlock.
    proc=subprocess.Popen(cmd,stdin=subprocess.PIPE)
    try:
        for f in range(math.ceil(duration*FPS)):proc.stdin.write(g.frame(f/FPS).tobytes())
    finally:proc.stdin.close()
    if proc.wait():raise RuntimeError('Composition encoding failed')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('source',type=Path);p.add_argument('output',type=Path);p.add_argument('recipe',type=Path);p.add_argument('analysis',type=Path);a=p.parse_args()
    render(a.source,a.output,json.loads(a.recipe.read_text()),json.loads(a.analysis.read_text()))
