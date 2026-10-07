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
ALLOWED_FIELDS = {'id','hornId','maker','model','serialNumber','details','title','description','url','source','sourceListingId','seller','location','price','currency','shipping','postedAt','status','discoveryType','images','searchScore','searchRationale','tags','evidence','verificationState'}
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


def fetch(url, headers=None, data=None, timeout=25):
    safe_remote(url)
    req = urllib.request.Request(url, data=data, headers={'User-Agent': USER_AGENT, **(headers or {})})
    with urllib.request.build_opener(SafeRedirect()).open(req, timeout=timeout) as response:
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
    subject=re.split(r'\s[|–—]\s',title,maxsplit=1)[0].lower()
    if re.search(r'\b(cornets?|flugelhorns?|trombones?|mouthpieces?|mutes?|stands?|valve oil|cleaning kit|trim kit|t-shirts?|case only|piccolo)\b',subject): return None
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


class DealerPage(HTMLParser):
    """Individual product metadata; no scraping prices from related cards."""
    def __init__(self):
        super().__init__(); self.meta={}; self.title=[]; self.in_title=False
    def handle_starttag(self, tag, attrs):
        a=dict(attrs)
        if tag=='title': self.in_title=True
        if tag=='meta':
            key=a.get('property') or a.get('name') or a.get('itemprop')
            if key and a.get('content'): self.meta.setdefault(key.lower(),[]).append(a['content'])
    def handle_endtag(self, tag):
        if tag=='title': self.in_title=False
    def handle_data(self, data):
        if self.in_title: self.title.append(data)
    def one(self, *keys):
        for key in keys:
            values=set(self.meta.get(key,[]))
            if len(values)==1: return next(iter(values))
        return ''
    def name(self): return self.one('og:title') or ''.join(self.title)


def page_offers(page, url, source, profile):
    structured=products(page)
    if len(structured)>1: return []  # related products cannot verify the requested horn
    if structured:
        return [c for p in structured if (c:=normalize_product(p,url,source,profile))]
    # BigCommerce/OpenGraph dealer metadata must explicitly establish all
    # three offer facts. Missing availability stays in the candidate queue.
    metadata=DealerPage(); metadata.feed(page)
    availability=metadata.one('product:availability','availability')
    availability={'in stock':'InStock','instock':'InStock','out of stock':'OutOfStock','outofstock':'OutOfStock','sold out':'SoldOut'}.get(availability.lower(),availability)
    p={'name':metadata.name(),'description':metadata.one('og:description','description'),
       'image':metadata.one('og:image'),'offers':{'price':metadata.one('product:price:amount','price'),
       'priceCurrency':metadata.one('product:price:currency','pricecurrency'),'availability':availability}}
    c=normalize_product(p,url,source,profile)
    return [dict(c,evidence='Individual dealer product metadata / explicit availability')] if c else []


def shopify_offer(url, source, profile, get_page=fetch):
    """Shopify dealers: explicit variant stock, price and currency required."""
    parts=urllib.parse.urlsplit(url)
    if not re.search(r'/products/[^/]+/?$',parts.path): return None
    base=urllib.parse.urlunsplit((parts.scheme,parts.netloc,parts.path.rstrip('/'),'',''))
    product=json.loads(get_page(base+'.js'))
    variants=product.get('variants',[])
    if not variants or any(not isinstance(v.get('available'),bool) for v in variants): return None
    available=[v for v in variants if v['available']]
    prices={v.get('price') for v in (available or variants)}
    if len(prices)!=1 or not isinstance(next(iter(prices)),(float,int)): return None
    currency=json.loads(get_page(urllib.parse.urlunsplit((parts.scheme,parts.netloc,'/cart.js','','')))).get('currency','')
    if currency not in ('USD','EUR','GBP','CAD','AUD','CHF','JPY'): return None
    # Shopify appends hundredths even for currencies without subunits:
    # https://shopify.dev/docs/api/liquid/objects/product#product-price
    # Its documented 1000 JPY example is represented as 100000.
    price=next(iter(prices))/100
    p={'name':product.get('title',''),'description':product.get('description',''),'brand':product.get('vendor',''),
       'productID':str(product.get('id','')),'image':product.get('images',[]),
       'offers':{'price':price,'priceCurrency':currency,'availability':'InStock' if available else 'SoldOut'}}
    c=normalize_product(p,url,source,profile)
    return dict(c,evidence='Shopify product JSON / explicit variant stock and cart currency') if c else None


def candidate_hint(result, source, profile, page=''):
    url=result.get('url',''); parts=urllib.parse.urlsplit(url)
    if parts.scheme!='https' or not parts.hostname or parts.username or parts.password: return None
    path=urllib.parse.unquote(parts.path).lower()
    if path.rstrip('/').rsplit('/',1)[-1] in ('for-sale','for_sale','forsale','inventory','used','shop','trumpets','trumpet'): return None
    # Category pages, research articles and generic shop roots are not offers.
    if not re.search(r'/products?/[^/]+|/itm/|/item/|/listings?/|/classifieds?/|/m[0-9]+|/[^/]*(?:trumpet|taylor|harrelson|oiram|dorotea|feroce|calicchio|lawler|monette)[^/]+',path): return None
    if re.search(r'/blogs?/|/news/|/collections/[^/]+/?$|/categor',path): return None
    metadata=DealerPage(); metadata.feed(page)
    page_title=metadata.name()
    if not any(m.lower() in page_title.lower() for m in MAKERS): page_title=''
    title=html.unescape(page_title or result.get('title','') or re.sub(r'[-_]+',' ',parts.path.rstrip('/').split('/')[-1]))[:500]
    description=result.get('description','')
    text=(title+' '+description+' '+path.replace('-',' ')).lower()
    maker=next((m for m in MAKERS if re.search(r'\b'+re.escape(m.lower())+r'\b',text)),None)
    subject=re.split(r'\s[|–—]\s',title,maxsplit=1)[0].lower()+' '+path.replace('-',' ')
    if not maker or re.search(r'\b(mouthpieces?|flugelhorns?|cornets?|trombones?|mutes?|stands?|valve oil|cleaning kit|trim kit|t-shirts?|piccolo|case only|c trumpet|eb trumpet|d trumpet)\b',subject): return None
    # Reuse ranking without treating the temporary scoring offer as evidence.
    scoring=normalize_product({'name':title+' trumpet','description':text,'offers':{'priceCurrency':'USD','availability':'InStock'}},url,source,profile)
    if not scoring or scoring['searchScore']<45: return None
    return dict(maker=maker,model=title,title=title,description='',url=url,source=source,
        status='stale',verificationState='candidate',discoveryType='newly discovered',price=None,
        currency='USD',shipping=None,postedAt=None,serialNumber='',details={},images=[],
        searchScore=scoring['searchScore'],tags=scoring['tags'],
        searchRationale='Promising search lead: '+maker+'. Price, availability and instrument details require verification.',
        evidence='Unverified search result / URL hint; not a market observation')


MAKER_QUERIES = [
    'Taylor Chicago II Chicago 46 upswept raw brass used Bb trumpet sale',
    'Harrelson MUSE Bravura Summit used Bb trumpet sale',
    'AR Resonance Feroce Monette used Bb trumpet sale',
    'Adams A4 A9 Blackburn custom used Bb trumpet sale',
    'Van Laar OIRAM Inderbinen used Bb trumpet sale',
    'Lawler C7 LOTUS Solo Max Eclipse used Bb trumpet sale',
    'Del Quadro Dorotea BAC Calicchio used Bb trumpet sale',
    'Schilke Handcraft HC1 HC2 Faddis gold Benge custom used Bb trumpet sale',
    'GERDT Lars Hjalt Yamaha 921X Selmer Concept TT Olds Super Recording used trumpet sale',
]


def recheck(listing, get_page, profile):
    c={k:copy.deepcopy(v) for k,v in listing.items() if k in ALLOWED_FIELDS}
    c['discoveryType']='rediscovered'
    if not c.get('url'): return None
    try: page=get_page(c['url'])
    except urllib.error.HTTPError as error:
        if error.code not in (404,410): raise
        if c.get('verificationState')=='candidate': return None
        c.update(status='removed',evidence=f'HTTP {error.code}; prior price retained as last known')
        return c
    candidates=page_offers(page,c['url'],c['source'],profile)
    if not candidates and '/products/' in c['url']:
        try:
            offer=shopify_offer(c['url'],c['source'],profile,get_page)
            if offer: candidates=[offer]
        except Exception: pass
    if c.get('verificationState')=='candidate' and len(candidates)==1:
        return dict(candidates[0],id=c.get('id'),verificationState='verified')
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


def search_openai(query, key):
    """Use actual web-tool sources, never model-generated URLs or offer facts."""
    domains=re.findall(r'(?<!-)\bsite:([a-zA-Z0-9.-]+)',query)
    excluded_domains=re.findall(r'-site:([a-zA-Z0-9.-]+)',query)
    tool={'type':'web_search','search_context_size':'low'}
    request={'model':os.environ.get('TRUMPETS_SEARCH_MODEL','gpt-4.1-mini'),
             'tools':[tool],'tool_choice':'required',
             'include':['web_search_call.action.sources'],
             'max_output_tokens':1000,
             'input':'Find specific used professional Bb trumpet listing pages for this query. '
                     'Search the web; do not invent URLs, prices or availability. '
                     'Return a concise list of URLs without descriptions. '+query}
    for attempt in range(3):
        try:
            payload=json.loads(fetch('https://api.openai.com/v1/responses',
                {'Authorization':'Bearer '+key,'Content-Type':'application/json'},
                json.dumps(request).encode(),timeout=120))
            break
        except urllib.error.HTTPError as error:
            if error.code not in (408,429,500,502,503,504) or attempt==2: raise
        except (urllib.error.URLError,TimeoutError):
            if attempt==2: raise
        time.sleep(2*(attempt+1))
    if payload.get('status')!='completed': raise ValueError('Incomplete web research response')
    titles={}
    for message in payload.get('output',[]):
        if message.get('type')=='message':
            for content in message.get('content',[]):
                for annotation in content.get('annotations',[]):
                    if annotation.get('type')=='url_citation' and annotation.get('title'):
                        titles[annotation.get('url','')]=annotation['title']
    results=[];seen=set();searched=False
    for item in payload.get('output',[]):
        if item.get('type')!='web_search_call' or item.get('status')!='completed': continue
        action=item.get('action',{})
        if action.get('type')!='search': continue
        searched=True
        for source in action.get('sources',[]):
            url=source.get('url','')
            host=(urllib.parse.urlsplit(url).hostname or '').lower()
            if any(host==d or host.endswith('.'+d) for d in excluded_domains): continue
            if domains and not any(host==d or host.endswith('.'+d) for d in domains): continue
            if url.startswith('https://') and url not in seen:
                seen.add(url);title=source.get('title') or titles.get(url)
                results.append({'url':url, **({'title':title} if title else {})})
    if not searched: raise ValueError('No completed web search')
    return results[:10]


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
    board=client.call('/listings')['listings']
    excluded={l.get('url') for l in board if l.get('acquired') or l.get('feedback',{}).get('interestState')=='pass'}
    report={'externalId':'daily-coverage-v2-'+dt.datetime.now(dt.timezone.utc).strftime('%Y-%m-%d'), 'kind':'combined', 'status':'succeeded','error':'','sources':[],'listings':[]}
    issues=[];checks={};seen=set(excluded); started=time.monotonic(); inspected=0
    def check(name): return checks.setdefault(name,dict(source=name,status='checked',candidates=0,note=''))
    queued_checked=0
    for listing in sorted(due,key=lambda l:l.get('verificationState')=='candidate'):
        if listing.get('verificationState')=='candidate':
            if queued_checked>=40 or time.monotonic()-started>2100: continue
            queued_checked+=1
        if not listing.get('url'): continue  # incomplete historical references
        s=check(listing['source'])
        try:
            c=recheck(listing,fetch,profile)
            if c: report['listings'].append(c);s['candidates']+=1;seen.add(c['url'])
            elif listing.get('verificationState')!='candidate': s['status']='failed';s['note']='One or more pages lacked an unambiguous structured offer; no price/status guessed.';issues.append('Unsupported recheck at '+s['source'])
        except Exception:
            if listing.get('verificationState')!='candidate':
                s['status']='failed';s['note']='Recheck denied, unavailable or invalid; prior observations preserved.';issues.append('Failed recheck at '+s['source'])
    key=os.environ.get('BRAVE_SEARCH_API_KEY')
    openai_key=os.environ.get('OPENAI_API_KEY')
    groups=list(SOURCES.items())+[('New source discovery',None),('Credible private listings',None)]+[(f'Maker search {i+1}',None) for i in range(len(MAKER_QUERIES))]
    for name,domains in groups:
        s=check(name)
        if time.monotonic()-started>2100:
            s['status']='skipped';s['note']='Run time budget reached; search deferred.';issues.append('Discovery time budget reached');continue
        if not key and not openai_key:
            s['status']='skipped';s['note']='No web search provider is configured.';continue
        query=(' OR '.join('site:'+d for d in domains)+' used boutique Bb trumpet') if domains else ('used custom Bb trumpet Taylor Harrelson Van Laar boutique dealer -site:reverb.com' if name=='New source discovery' else 'used boutique Bb trumpet private sale custom')
        if name.startswith('Maker search '): query=MAKER_QUERIES[int(name.rsplit(' ',1)[-1])-1]+' -site:reverb.com'
        try:
            results=search(query,key) if key else search_openai(query,openai_key)
            unsupported=0
            for result in results:
                url=result.get('url','')
                if url in seen: continue
                # Source discovery retains the actual domain, not the discovery category.
                source=name if domains else urllib.parse.urlsplit(url).hostname or name
                page=''; candidates=[]
                if inspected<240 and time.monotonic()-started<2100:
                    inspected+=1
                    try:
                        page=fetch(url)
                        candidates=page_offers(page,url,source,profile)
                        if not candidates and '/products/' in url:
                            try:
                                offer=shopify_offer(url,source,profile)
                                if offer: candidates=[offer]
                            except Exception: pass
                    except Exception: pass
                if len(candidates)==1:
                    c=candidates[0]
                    if c['searchScore']>=55 and c['status']=='active':
                        report['listings'].append(c);seen.add(url);s['candidates']+=1
                else:
                    unsupported+=1
                    c=candidate_hint(result,source,profile,page)
                    if c:
                        report['listings'].append(c);seen.add(url);s['candidates']+=1
            s['note']=f'{len(results)} search results reviewed; {unsupported} lacked verified individual offers. Promising unverified leads queued separately; coverage is not exhaustive.'
            time.sleep(1.1)  # modest per-run provider pacing
        except Exception:
            s['status']='failed';s['note']='Search provider request failed.';issues.append('Search failed at '+name)
        print(json.dumps({'source':name,'status':s['status'],'candidates':s['candidates']}),flush=True)
    if not key and not openai_key: issues.append('Broad search not run: web search provider missing.')
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
