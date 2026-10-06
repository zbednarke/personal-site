#!/usr/bin/env python3
"""Daily search/revalidation and ChatGPT report bridge. Python stdlib only.
Secrets are environment variables; feedback is never logged or cached.
"""
import argparse
import copy
import datetime as dt
import html
from html.parser import HTMLParser
import ipaddress
import json
import os
import re
import socket
import time
import urllib.error
import urllib.parse
import urllib.request

SOURCES = {
    'Reverb': ['reverb.com'], 'HornTrader': ['horntrader.com'],
    'Austin Custom Brass': ['austincustombrass.biz'], 'Thompson Music': ['thompsonmusic.com'],
    'J. Landress Brass': ['jlandressbrass.com'], 'Dillon Music': ['dillonmusic.com'],
    'Trent Austin': ['trentaustin.com'], 'Baltimore Brass': ['baltimorebrass.net'],
    'Rich Ita': ['brassinstrumentworkshop.com'], 'Brass Ark': ['brassark.com'],
    'Horn Stash': ['hornstash.com'], 'Brass Exchange': ['thebrass-exchange.com'],
    'Mighty Quinn': ['brassandwinds.com'], 'Ferguson Music': ['hornguys.com'],
    'Gamonbrass': ['gamonbrass.com'], 'Dawkes': ['dawkes.co.uk'],
    'Windblowers': ['windblowers.com'], 'Phil Parker': ['philparkerltd.com'],
    'Trevor Jones': ['trevorjonesltd.co.uk'], 'John Packer': ['johnpacker.co.uk'],
    'TC Gakki / Japanese shops': ['tcgakki.com', 'ishibashi.co.jp'],
    'European specialist dealers': ['adams-music.com', 'brassfeeling.de'],
    'eBay': ['ebay.com', 'ebay.co.uk'], 'Marktplaats': ['marktplaats.nl'],
    'Auction houses': ['invaluable.com', 'catawiki.com'],
    'Regional music shops': ['musicgoround.com'],
    'Maker / demo inventory': ['taylortrumpets.com', 'whyharrelson.com', 'arresonance.com'],
}
# No private marketplace login/session reuse. User agents honor denied pages.
USER_AGENT = 'TrumpetObservatory/1.0 (+private instrument research; daily checks)'
ALLOWED_FIELDS = {'id','hornId','maker','model','serialNumber','details','title','description','url','source','sourceListingId','seller','location','price','currency','shipping','postedAt','status','discoveryType','images','searchScore','searchRationale','tags','evidence'}
MAKERS = ['AR Resonance','Del Quadro','Van Laar','Harrelson','Monette','Inderbinen','Blackburn','Calicchio','Schilke','Taylor','Adams','Lawler','LOTUS','Eclipse','Benge','GERDT','BAC','Olds','Selmer','Besson','Yamaha','Bach']
TRAITS = ['raw brass','raw nickel','aged brass','patina','engraving','gold plate','mixed metals','black hardware','upswept','one-off','artist','provenance','custom','handcraft','oiram','antique']


def safe_remote(url):
    p = urllib.parse.urlsplit(url)
    if p.scheme != 'https' or not p.hostname or p.username or p.password or p.port not in (None, 443):
        raise ValueError('Only public HTTPS URLs allowed')
    for result in socket.getaddrinfo(p.hostname, 443, type=socket.SOCK_STREAM):
        if not ipaddress.ip_address(result[4][0]).is_global:
            raise ValueError('Private network destination rejected')
    return url


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        safe_remote(newurl)
        if urllib.parse.urlsplit(req.full_url).netloc != urllib.parse.urlsplit(newurl).netloc and any(k.lower() in ('authorization','x-subscription-token') for k in req.headers):
            raise ValueError('Authenticated cross-origin redirect rejected')
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def fetch(url, headers=None, data=None):
    safe_remote(url)
    req = urllib.request.Request(url, data=data, headers={'User-Agent': USER_AGENT, **(headers or {})})
    with urllib.request.build_opener(SafeRedirect()).open(req, timeout=25) as response:
        content = response.read(3_000_001)
        if len(content) > 3_000_000:
            raise ValueError('Response too large')
        return content.decode('utf-8', errors='replace')


class StructuredPage(HTMLParser):
    def __init__(self):
        super().__init__(); self.scripts=[]; self.current=None
    def handle_starttag(self, tag, attrs):
        if tag=='script' and dict(attrs).get('type','').lower()=='application/ld+json':
            self.current=[]
    def handle_data(self, data):
        if self.current is not None: self.current.append(data)
    def handle_endtag(self, tag):
        if tag=='script' and self.current is not None:
            try: self.scripts.append(json.loads(''.join(self.current)))
            except (ValueError, TypeError): pass
            self.current=None


def products(page):
    parser=StructuredPage(); parser.feed(page)
    def walk(node):
        if isinstance(node,list):
            for child in node: yield from walk(child)
        elif isinstance(node,dict):
            types=node.get('@type',[])
            if types=='Product' or (isinstance(types,list) and 'Product' in types): yield node
            elif '@graph' in node: yield from walk(node['@graph'])
    return [p for script in parser.scripts for p in walk(script)]


def amount(value):
    try:
        value=float(value)
        return round(value,2) if 0<=value<=999999999999.99 else None
    except (ValueError,TypeError): return None


def normalize_product(product,url,source,profile):
    title=html.unescape(str(product.get('name','')))
    description=html.unescape(re.sub('<[^>]+>',' ',str(product.get('description',''))))[:50000]
    text=(title+' '+description).lower()
    if not re.search(r'\btrumpet\b|\btrompette\b|トランペット|\btrompet\b',text): return None
    if re.search(r'\b(cornet|flugelhorn|trombone|mouthpiece|case only|piccolo)\b',title.lower()): return None
    if re.search(r'\b(c trumpet|eb trumpet|d trumpet|e-flat|c-trumpet)\b',title.lower()): return None
    maker=next((m for m in MAKERS if re.search(r'\b'+re.escape(m.lower())+r'\b',text)),None)
    brand=product.get('brand',{})
    if isinstance(brand,dict): brand=brand.get('name','')
    if not maker: maker=str(brand or '')
    if not maker: return None
    offers=product.get('offers') or {}
    if isinstance(offers,list): offers=offers[0] if offers else {}
    if not isinstance(offers,dict): return None
    # Aggregate offers cannot establish a specific horn's price/availability.
    if offers.get('@type')=='AggregateOffer': return None
    availability=str(offers.get('availability','')).lower().rsplit('/',1)[-1]
    if availability in ('instock','limitedavailability','onlineonly'): status='active'
    elif availability in ('soldout','outofstock','discontinued'): status='sold'
    else: return None
    price=amount(offers.get('price'))
    currency=str(offers.get('priceCurrency','')).upper()
    if not re.fullmatch('[A-Z]{3}',currency): return None
    traits=[v for v in TRAITS if v in text]
    priority={m.lower() for m in profile.get('priorityMakers',[])}
    base=65 if maker.lower() in priority else 35
    if maker=='Adams' and not re.search(r'\ba[49]\b|custom',text): base=40
    if maker=='Benge' and not traits and not re.search(r'rare|unusual|vintage|custom',text): base=40
    exceptional_model=re.search(r'handcraft|\bhc[12]\b|921x|faddis|super recording|opera premiere|concept tt|\bmeha\b',text)
    if exceptional_model: base=max(base,65)
    if maker=='Bach' and 'blackburn' in text: base=65
    if maker=='Schilke' and 'gold plate' in text: base=max(base,60)
    if maker in ('Yamaha','Bach') and not any(t in traits for t in ('gold plate','custom','provenance','one-off')) and '921x' not in text: return None
    favored=profile.get('favoredAttributes',{}); disliked=profile.get('dislikedAttributes',{})
    bonus=sum(min(5,weight)*2 for term,weight in favored.items() if term in text)
    penalty=sum(min(5,weight)*3 for term,weight in disliked.items() if term in text)
    score=max(0,min(100,base+len(traits)*5+bonus-penalty))
    images=product.get('image',[])
    if isinstance(images,(str,dict)): images=[images]
    images=[i.get('url','') if isinstance(i,dict) else i for i in images]
    images=[i for i in images if isinstance(i,str) and i.startswith('https://')][:20]
    model=str(product.get('model') or title)
    if isinstance(product.get('model'),dict): model=str(product['model'].get('name') or title)
    # Scores are evidence-based triage. Freeform notes remain available in the
    # profile to the ChatGPT workflow; this baseline does not pretend to interpret them.
    serial=str(product.get('serialNumber') or '')
    finish=next((t for t in traits if t in ('raw brass','raw nickel','gold plate','antique','aged brass')),'')
    seller=offers.get('seller',{})
    seller=seller.get('name','') if isinstance(seller,dict) else str(seller)
    posted=None
    published=product.get('datePublished') or product.get('datePosted')
    if isinstance(published,str):
        try:
            date=dt.datetime.fromisoformat(published.replace('Z','+00:00'))
            if date.tzinfo is None: date=date.replace(tzinfo=dt.timezone.utc)
            posted=date.isoformat()
        except ValueError: pass
    now=dt.datetime.now(dt.timezone.utc)
    new=posted is not None and now-dt.timedelta(days=1)<=dt.datetime.fromisoformat(posted)<=now
    return dict(maker=maker,model=model,title=title,description=description,url=url,source=source,
                sourceListingId=str(product.get('productID') or ''),seller=seller,location='',
                serialNumber=serial,details={'finish':finish},price=price,currency=currency,shipping=None,
                status=status,discoveryType='new listing' if new else 'newly discovered',postedAt=posted,images=images,searchScore=score,
                searchRationale=f'{maker}; '+(', '.join(traits) or 'professional instrument reference')+'. Structured offer verified; shipping and condition require review.',
                tags=traits,evidence='Product JSON-LD offer / '+availability)


def recheck(listing, get_page, profile):
    c={k:copy.deepcopy(v) for k,v in listing.items() if k in ALLOWED_FIELDS}
    c['discoveryType']='rediscovered'
    if not c.get('url'): return None
    try: page=get_page(c['url'])
    except urllib.error.HTTPError as error:
        if error.code not in (404,410): raise
        c.update(status='removed',evidence=f'HTTP {error.code}; prior price retained as last known')
        return c
    candidates=[normalize_product(p,c['url'],c['source'],profile) for p in products(page)]
    candidates=[p for p in candidates if p]
    # A page with multiple unrelated products is ambiguous; never take the
    # price of a recommendation/related product as the tracked horn's price.
    matched=[p for p in candidates if p['title'].strip().lower()==c['title'].strip().lower() or (p['sourceListingId'] and p['sourceListingId']==c.get('sourceListingId'))]
    if len(matched)!=1: return None
    p=matched[0]
    c.update(status=p['status'],price=p['price'] if p['price'] is not None else c.get('price'),currency=p['currency'],evidence=p['evidence'])
    if p['images']: c['images']=p['images']
    return c


def search(query,key):
    params=urllib.parse.urlencode({'q':query,'count':10,'search_lang':'en'})
    payload=json.loads(fetch('https://api.search.brave.com/res/v1/web/search?'+params,{'X-Subscription-Token':key,'Accept':'application/json'}))
    return payload.get('web',{}).get('results',[])


class Client:
    def __init__(self):
        self.base=os.environ['TRUMPETS_API_URL'].rstrip('/')+'/v1/trumpets/machine'
        self.token=os.environ['TRUMPETS_MACHINE_TOKEN']
    def call(self,path,data=None):
        headers={'Authorization':'Bearer '+self.token,'Accept':'application/json'}
        if data is not None: headers['Content-Type']='application/json'
        return json.loads(fetch(self.base+path,headers,None if data is None else json.dumps(data).encode()))


def daily(client):
    profile=client.call('/profile'); due=client.call('/due')['listings']
    report={'externalId':'daily-'+dt.datetime.now(dt.timezone.utc).strftime('%Y-%m-%d'), 'kind':'combined', 'status':'succeeded','error':'','sources':[],'listings':[]}
    issues=[];checks={};seen=set()
    def check(name): return checks.setdefault(name,dict(source=name,status='checked',candidates=0,note=''))
    for listing in due:
        if not listing.get('url'): continue  # incomplete historical references
        s=check(listing['source'])
        try:
            c=recheck(listing,fetch,profile)
            if c: report['listings'].append(c);s['candidates']+=1;seen.add(c['url'])
            else: s['status']='failed';s['note']='One or more pages lacked an unambiguous structured offer; no price/status guessed.';issues.append('Unsupported recheck at '+s['source'])
        except Exception:
            s['status']='failed';s['note']='Recheck denied, unavailable or invalid; prior observations preserved.';issues.append('Failed recheck at '+s['source'])
    key=os.environ.get('BRAVE_SEARCH_API_KEY')
    groups=list(SOURCES.items())+[('New source discovery',None),('Credible private listings',None)]
    for name,domains in groups:
        s=check(name)
        if not key:
            s['status']='skipped';s['note']='BRAVE_SEARCH_API_KEY is not configured.';continue
        query=(' OR '.join('site:'+d for d in domains)+' used boutique Bb trumpet') if domains else ('used custom Bb trumpet Taylor Harrelson Van Laar boutique dealer -site:reverb.com' if name=='New source discovery' else 'used boutique Bb trumpet private sale custom')
        try:
            results=search(query,key)
            unsupported=0
            for result in results:
                url=result.get('url','')
                if url in seen: continue
                # Source discovery retains the actual domain, not the discovery category.
                source=name if domains else urllib.parse.urlsplit(url).hostname or name
                try:
                    candidates=[normalize_product(p,url,source,profile) for p in products(fetch(url))]
                    candidates=[p for p in candidates if p]
                    if len(candidates)!=1: unsupported+=1;continue
                    c=candidates[0]
                    if c['searchScore']>=55 and c['status']=='active':
                        report['listings'].append(c);seen.add(url);s['candidates']+=1
                except Exception: unsupported+=1
            s['note']=f'{len(results)} search results checked; {unsupported} pages unsupported or unavailable. Structured individual offers only.'
            time.sleep(1.1)  # modest per-run provider pacing
        except Exception:
            s['status']='failed';s['note']='Search provider request failed.';issues.append('Search failed at '+name)
    if not key: issues.append('Broad search not run: BRAVE_SEARCH_API_KEY missing.')
    if issues: report['status']='partial';report['error']=' '.join(sorted(set(issues)))[:10000]
    report['sources']=list(checks.values())
    result=client.call('/runs',report)
    print(json.dumps({'runId':result['runId'],'status':result.get('status','replayed'),'remainingActive':result.get('remainingActive'),'submitted':len(report['listings'])}))
    return 0 if result.get('status')=='succeeded' else 2


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--report',help='Submit a normalized ChatGPT run JSON; retries use the same externalId.')
    parser.add_argument('--prepare',help='Write a private profile/all-tracked/due snapshot for a search client.')
    args=parser.parse_args();client=Client()
    if args.prepare:
        snapshot={key:client.call('/'+path) for key,path in [('profile','profile'),('board','listings'),('due','due')]}
        fd=os.open(args.prepare,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
        os.fchmod(fd,0o600)
        with os.fdopen(fd,'w') as out: json.dump(snapshot,out,indent=2)
        return 0
    if args.report:
        with open(args.report) as source: report=json.load(source)
        result=client.call('/runs',report);print(json.dumps(result));return 0
    return daily(client)

if __name__=='__main__':
    raise SystemExit(main())
