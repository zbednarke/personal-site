#!/usr/bin/env python3
"""Daily search/revalidation and ChatGPT report bridge. Python stdlib only.
Secrets are environment variables; feedback is never logged or cached.
"""
import argparse
import copy
import datetime as dt
import html
import hashlib
import collections
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
TRAITS = ['raw brass','raw nickel','aged brass','patina','engraving','gold plate','mixed metals','black hardware','upswept','one-off','artist','provenance','custom','handcraft','oiram','antique','engineering','rare','vintage','unusual bell','artist model','prototype','discontinued']


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


def canonical_url(url):
    p=urllib.parse.urlsplit(url)
    tracking={'fbclid','gclid','ref','referrer','srsltid','itmmeta','itmprp','hash','_trksid','_trkparms'}
    query=urllib.parse.urlencode(sorted((k,v) for k,v in urllib.parse.parse_qsl(p.query) if not k.lower().startswith('utm_') and k.lower() not in tracking))
    return urllib.parse.urlunsplit(('https',p.netloc.lower().removeprefix('www.'),p.path.rstrip('/'),query,''))


def listing_id(url):
    p=urllib.parse.urlsplit(url)
    if 'ebay.' in p.hostname:
        match=re.search(r'/itm/(?:[^/]+/)?([0-9]{9,})',p.path)
        return match[1] if match else ''
    if p.hostname.removeprefix('www.')=='reverb.com':
        match=re.search(r'/item/([0-9]+)',p.path)
        return match[1] if match else ''
    return ''


def shipping_amount(offer,currency):
    shipping=offer.get('shippingDetails',{})
    if isinstance(shipping,list): shipping=shipping[0] if len(shipping)==1 else {}
    rate=shipping.get('shippingRate',{}) if isinstance(shipping,dict) else {}
    return amount(rate.get('value')) if isinstance(rate,dict) and rate.get('currency')==currency else None


def address_text(value):
    if not isinstance(value,dict): return ''
    address=value.get('address',value)
    if not isinstance(address,dict): return ''
    return ', '.join(str(address[k]) for k in ('addressLocality','addressRegion','addressCountry') if isinstance(address.get(k),str))


def link_physical_horn(candidate,baseline):
    """Only serials or identical images plus matching model link automatically.
    Similar configurations remain separate; the API offers possible-relist hints.
    """
    for old in baseline:
        if old.get('verificationState')=='candidate' or old['maker'].lower()!=candidate['maker'].lower(): continue
        serial=candidate.get('serialNumber','').replace(' ','').lower()
        old_serial=old.get('serialNumber','').replace(' ','').lower()
        if serial and old_serial and serial!=old_serial: continue
        serial_match=bool(serial and serial==old_serial)
        image_match=old['model'].lower()==candidate['model'].lower() and bool(old.get('seller')) and old.get('seller','').lower()==candidate.get('seller','').lower() and bool(set(old.get('images',[])) & set(candidate.get('images',[])))
        if (serial_match or image_match) and old.get('hornId'):
            candidate['hornId']=old['hornId']; return candidate
    return candidate


def identify_maker(text):
    matches=[]
    for maker in MAKERS:
        match=re.search(r'\b'+re.escape(maker.lower())+r'\b',text.lower())
        if match:matches.append((match.start(),maker))
    return min(matches)[1] if matches else None


def normalize_product(product,url,source,profile):
    title=html.unescape(str(product.get('name','')))
    description=html.unescape(re.sub('<[^>]+>',' ',str(product.get('description',''))))[:50000]
    text=(title+' '+description).lower()
    if not re.search(r'\btrumpet\b|\btrompette\b|トランペット|\btrompet\b',text): return None
    subject=re.split(r'\s[|–—]\s',title,maxsplit=1)[0].lower()
    if re.search(r'\b(cornets?|flugelhorns?|trombones?|mouthpieces?|mutes?|stands?|valve oil|cleaning kit|trim kit|t-shirts?|case only|piccolo)\b',subject): return None
    if re.search(r'\b(c trumpet|eb trumpet|d trumpet|e-flat|c-trumpet)\b',title.lower()): return None
    maker=identify_maker(title) or identify_maker(description)
    brand=product.get('brand',{})
    if isinstance(brand,dict): brand=brand.get('name','')
    if isinstance(brand,str) and brand.lower() in {m.lower() for m in MAKERS}: maker=next(m for m in MAKERS if m.lower()==brand.lower())
    if not maker: maker=str(brand or '')
    if not maker: return None
    offers=product.get('offers') or {}
    if isinstance(offers,list):
        facts={(str(o.get('price')),str(o.get('priceCurrency')),str(o.get('availability'))) for o in offers if isinstance(o,dict)}
        if len(facts)!=1 or len(offers)!=sum(isinstance(o,dict) for o in offers): return None
        offers=offers[0] if offers else {}
    if not isinstance(offers,dict): return None
    # Aggregate offers cannot establish a specific horn's price/availability.
    if offers.get('@type')=='AggregateOffer': return None
    availability=str(offers.get('availability','')).lower().rsplit('/',1)[-1]
    if availability in ('instock','limitedavailability','onlineonly'): status='active'
    elif availability in ('soldout','outofstock','discontinued'): status='sold'
    elif availability in ('preorder','presale','backorder','reserved','pending','temporarilyunavailable'): status='stale'
    else: return None
    price=amount(offers.get('price'))
    currency=str(offers.get('priceCurrency','')).upper()
    if not re.fullmatch('[A-Z]{3}',currency): return None
    traits=[v for v in TRAITS if v in text]
    priority={m.lower() for m in profile.get('priorityMakers',[])}
    base=65 if maker.lower() in priority else 35
    if any(t in traits for t in ('engineering','rare','prototype','one-off','unusual bell','custom','provenance')): base=max(base,60)
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
    notes=profile.get('notePreferences',{})
    bonus+=sum(min(3,weight)*2 for term,weight in notes.get('favoredAttributes',{}).items() if term in text)
    penalty+=sum(min(3,weight)*2 for term,weight in notes.get('dislikedAttributes',{}).items() if term in text)
    bonus+=max(-6,min(6,notes.get('makerWeights',{}).get(maker.lower(),0)*2))
    for preference in notes.get('pricePreferences',[]):
        if preference['maker'].lower()==maker.lower() and preference['currency']==currency and price is not None and price>=preference['referencePrice']:
            penalty+=8; break
    score=max(0,min(100,base+len(traits)*5+min(12,bonus)-min(18,penalty)))
    images=product.get('image',[])
    if isinstance(images,(str,dict)): images=[images]
    images=[i.get('url','') if isinstance(i,dict) else i for i in images]
    images=[i for i in images if isinstance(i,str) and i.startswith('https://')][:20]
    model=str(product.get('model') or title)
    if isinstance(product.get('model'),dict): model=str(product['model'].get('name') or title)
    # Explicit private feedback phrases provide bounded ranking adjustments.
    serial=str(product.get('serialNumber') or '')
    if not serial:
        match=re.search(r'\bserial(?: number| no\.?)?\s*[:#]\s*([A-Za-z0-9][A-Za-z0-9-]{1,30})',description,re.I)
        if match: serial=match[1]
    condition=str(product.get('itemCondition','')).rsplit('/',1)[-1]
    properties=product.get('additionalProperty',[])
    if isinstance(properties,dict): properties=[properties]
    details={str(v.get('name','')).lower():str(v.get('value','')) for v in properties if isinstance(v,dict)}
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
                sourceListingId=str(product.get('productID') or listing_id(url)),seller=seller,location=address_text(offers.get('availableAtOrFrom') or product.get('location')),
                serialNumber=serial,details={'finish':finish,'condition':condition,**{k:details[k] for k in ('bore','bell','provenance') if k in details}},price=price,currency=currency,shipping=shipping_amount(offers,currency),
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
    maker=identify_maker(title) or identify_maker(text)
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


class LiveState(HTMLParser):
    def __init__(self):
        super().__init__();self.h1=[];self.badges=[];self.stack=[];self.skip=0
    def handle_starttag(self,tag,attrs):
        a=dict(attrs);label=(a.get('class','')+' '+a.get('id','')+' '+a.get('itemprop','')).lower()
        suppressed=self.skip>0 or tag in ('script','style','nav','footer') or bool(re.search(r'related|recommend|recently.viewed',label))
        kind='h1' if tag=='h1' else 'stock' if re.search(r'availability|stock.status|product.stock|sold.out|soldout',label) else ''
        if tag not in ('meta','link','img','br','hr','input','source','wbr'):
            self.stack.append((tag,kind,suppressed));self.skip+=int(suppressed)
    def handle_endtag(self,tag):
        for i in range(len(self.stack)-1,-1,-1):
            if self.stack[i][0]==tag:
                removed=self.stack[i:];self.stack=self.stack[:i];self.skip-=sum(int(v[2]) for v in removed);break
    def handle_data(self,data):
        if self.skip:return
        kinds={v[1] for v in self.stack}
        if 'h1' in kinds:self.h1.append(data)
        if 'stock' in kinds:self.badges.append(data)
    def state(self,title):
        heading=' '.join(' '.join(self.h1).split()).lower()
        target=' '.join(title.split()).lower()
        if not heading or (heading!=target and heading not in ('sold '+target,'sold: '+target,'sold - '+target)):return None
        text=' '.join(self.badges).lower()
        if heading.startswith('sold') or re.search(r'\b(sold|out of stock|expired|listing ended|discontinued)\b',text):return 'sold'
        if re.search(r'\b(pending|reserved|temporarily unavailable|backorder)\b',text):return 'stale'
        if re.search(r'\b(in stock|available now)\b',text):return 'active'
        return None


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
    state=LiveState();state.feed(page);authoritative=state.state(c['title'])
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
    if len(matched)!=1:
        if authoritative and c.get('verificationState')!='candidate':
            c.update(status=authoritative,evidence='Live product heading and explicit stock badge; prior price retained')
            return c
        return None
    p=matched[0]
    old_currency=c.get('currency')
    c.update(status=authoritative or p['status'],evidence=p['evidence'],searchScore=p['searchScore'],searchRationale=p['searchRationale'],tags=p['tags'])
    if p['price'] is not None:
        c.update(price=p['price'],currency=p['currency'])
        if p['currency']!=old_currency: c['shipping']=None
    elif p['currency']!=old_currency:
        c['evidence']+=' / current price unpublished; prior price and its original currency retained'
    if p.get('shipping') is not None and p['currency']==c.get('currency'):c['shipping']=p['shipping']
    for key in ('description','seller','location','serialNumber','postedAt'):
        if p.get(key) not in (None,''): c[key]=p[key]
    c['details']={**c.get('details',{}),**{k:v for k,v in p.get('details',{}).items() if v}}
    if authoritative: c['evidence']+=' / live product stock badge overrides metadata'
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


EXPLORATION_QUERIES = [
    'rare vintage professional Bb trumpet unusual engineering rotary bell custom provenance sale',
    'unfamiliar boutique trumpet builder prototype artist demo consignment Bb for sale',
    'trompette sib occasion artisan cuivre brut gravee rare collection',
    'Trompete B gebraucht Sonderanfertigung vergoldet selten Rohmessing',
    '中古 トランペット カスタム ビンテージ シリアル 販売',
    'GERDT Lars Hjalt custom trumpet used vintage Besson Meha Olds Opera',
    'Conn Connstellation Martin Committee Selmer Radial unusual rare Bb trumpet sale',
    'Kühnl Hoyer Ricco Kühn Schagerl Hub van Laar custom used trumpet demo',
]
NEW_SOURCE_QUERIES = [
    'used boutique Bb trumpets independent specialist consignment dealer Canada Australia',
    'rare custom trumpet private classifieds collector forum sale Europe UK',
    '中古 カスタム トランペット 販売 専門店 Japan vintage brass dealer',
    'trompette occasion artisan Trompete gebraucht custom specialist European shop',
    'trumpet maker artist demo prototype refurbished inventory boutique brass',
    'vintage brass trumpet estate auction regional music shop private seller',
    'rare trumpet consignment South America Scandinavia Switzerland dealer',
    'boutique professional trumpet used international enthusiast community sale',
]


def search_plan(profile,day=None):
    day=day or dt.datetime.now(dt.timezone.utc).date()
    universe=profile.get('sourceUniverse') or [dict(domain=d,name=n,specialty='specialist dealer',geography='International') for n,ds in SOURCES.items() for d in ds]
    # Sort never/oldest searched sources first; date-dependent ties avoid repeating
    # the same seed subset. Useful sources get a bounded portion of the run.
    rank=lambda s:hashlib.sha256((day.isoformat()+s['domain']).encode()).hexdigest()
    oldest=sorted(universe,key=lambda s:(s.get('lastSearched') or '',rank(s)))
    useful=sorted((s for s in universe if s.get('verifiedOffers',0)>0),key=rank)[:8]
    selected=[];families=collections.Counter()
    for source in useful+oldest:
        domain=source['domain']
        if any(s['domain']==domain for s in selected): continue
        family='ebay' if domain.startswith('ebay.') else domain
        if families[family]>=1: continue
        families[family]+=1;selected.append(source)
        if len(selected)==36: break
    offset=day.toordinal()
    plan=[]
    for i,source in enumerate(selected):
        explore=i%4==0
        phrase=EXPLORATION_QUERIES[(offset+i)%len(EXPLORATION_QUERIES)] if explore else MAKER_QUERIES[(offset+i)%len(MAKER_QUERIES)]
        plan.append({**source,'source':source['name']+' / '+source['domain'],'query':'site:'+source['domain']+' '+phrase,'exploration':explore})
    # All known domains are excluded from deliberate source-expansion queries.
    exclusions=''
    for source in universe:
        term=' -site:'+source['domain']
        if len(exclusions)+len(term)>3000: break
        exclusions+=term
    for i in range(4):
        plan.append(dict(source='New source discovery '+str(i+1),domain='',query=NEW_SOURCE_QUERIES[(offset*4+i)%len(NEW_SOURCE_QUERIES)]+' '+exclusions,exploration=True))
    for i in range(6):
        plan.append(dict(source='Maker search '+str(i+1),domain='',query=MAKER_QUERIES[(offset+i)%len(MAKER_QUERIES)]+' -site:reverb.com',exploration=False))
    for i in range(4):
        plan.append(dict(source='Exploration '+str(i+1),domain='',query=EXPLORATION_QUERIES[(offset+i)%len(EXPLORATION_QUERIES)]+' -site:reverb.com',exploration=True))
    return plan


def market_context(candidate,baseline):
    """Compare observed asking prices, never claim they are completed sales."""
    peers={}
    for old in baseline:
        if old.get('verificationState')!='verified' or not old.get('lastChecked') or old.get('price') is None:continue
        if old['maker'].lower()!=candidate['maker'].lower() or old['model'].lower()!=candidate['model'].lower() or old['currency']!=candidate['currency']:continue
        if old.get('hornId')==candidate.get('hornId') or old.get('url')==candidate.get('url'):continue
        peers.setdefault(old.get('hornId',old['id']),old['price'])
    if len(peers)<3 or candidate.get('price') is None:return candidate
    values=sorted(peers.values());median=(values[(len(values)-1)//2]+values[len(values)//2])/2
    if median<=0:return candidate
    ratio=candidate['price']/median
    if ratio<=.85:
        candidate['tags']=list(dict.fromkeys(candidate.get('tags',[])+['good value']))
        candidate['searchScore']=min(100,candidate['searchScore']+6)
    if ratio<=.85 or ratio>=1.25:
        candidate['searchRationale']+=f" Asking price is {abs(round((1-ratio)*100))}% {'below' if ratio<1 else 'above'} the median of {len(peers)} tracked same-model {candidate['currency']} asking prices ({median:g}); condition/configuration may differ, not completed-sale values."
    return candidate


def daily(client):
    profile=client.call('/profile'); due=client.call('/due')['listings']; board=client.call('/listings')
    baseline=board.get('listings',[])
    # Previously tracked sources remain part of the pool even if they predate
    # the normalized registry. Their next query/live check persists metadata.
    universe={s['domain']:s for s in profile.get('sourceUniverse',[])}
    for listing in baseline:
        if not listing.get('url'):continue
        domain=urllib.parse.urlsplit(listing['url']).hostname.removeprefix('www.')
        universe.setdefault(domain,dict(domain=domain,name=listing['source'],geography='International / unknown',specialty='Previously tracked source'))
    profile['sourceUniverse']=list(universe.values())
    known_domains=set(universe)
    # Daily due is also useful to external clients; each actual watch invocation
    # revalidates all active offers, including manual reruns on the same UTC day.
    due_by_id={l['id']:l for l in due}
    for listing in baseline:
        if listing['status']=='active' and not listing.get('acquired'):due_by_id[listing['id']]=listing
    due=list(due_by_id.values())
    by_url={canonical_url(l['url']):l for l in baseline if l.get('url')}
    report={'externalId':'adaptive-watch-'+dt.datetime.now(dt.timezone.utc).isoformat(),'startedAt':dt.datetime.now(dt.timezone.utc).isoformat(),
            'kind':'combined','status':'succeeded','sources':[],'listings':[],'error':''}
    failures=[];submitted=set();started=time.monotonic();candidate_checks=0;pages=0;domain_pages=collections.Counter();domain_finds=collections.Counter()
    live=collections.defaultdict(lambda:dict(pagesOpened=0,verifiedOffers=0,staleResults=0))
    key=os.environ.get('BRAVE_SEARCH_API_KEY');openai=os.environ.get('OPENAI_API_KEY')
    # Direct revalidation of every active offer takes precedence over discovery.
    for listing in sorted(due,key=lambda l:l.get('verificationState')=='candidate'):
        if not listing['url']: continue
        if listing.get('verificationState')=='candidate':
            if candidate_checks>=40 or time.monotonic()-started>35*60: continue
            candidate_checks+=1
        domain=urllib.parse.urlsplit(listing['url']).hostname.removeprefix('www.')
        try:
            checked=recheck(listing,fetch,profile)
            live[domain]['pagesOpened']+=1
            if checked:
                link_physical_horn(checked,baseline);market_context(checked,baseline);report['listings'].append(checked);submitted.add(canonical_url(listing['url']))
                live[domain]['verifiedOffers']+=1
                if checked['status']=='removed':live[domain]['staleResults']+=1
            elif listing.get('verificationState')!='candidate':failures.append('Active recheck unsupported: '+domain)
        except Exception as error:
            if listing.get('verificationState')!='candidate':failures.append('Active recheck failed: '+domain+' / '+type(error).__name__)
    if not key and not openai: failures.append('No search provider key configured')
    else:
        for task in search_plan(profile):
            check={k:task[k] for k in ('source','domain','query','geography','specialty') if k in task}
            check.update(status='checked',candidates=0,note='',pagesOpened=0,verifiedOffers=0,staleResults=0)
            if time.monotonic()-started>35*60:
                check.update(status='skipped',note='Run time budget exhausted');report['sources'].append(check);failures.append('Search time budget exhausted');continue
            try:
                results=search(task['query'],key) if key else search_openai(task['query'],openai)
                for result in results:
                    url=result.get('url','')
                    if not url.startswith('https://'):continue
                    canonical=canonical_url(url);old=by_url.get(canonical)
                    if canonical in submitted or (old and (old.get('acquired') or old.get('feedback',{}).get('interestState')=='pass' or old['status']=='active')):continue
                    domain=urllib.parse.urlsplit(url).hostname.removeprefix('www.')
                    if task['source'].startswith('New source discovery') and domain in known_domains:continue
                    if domain_finds[domain]>=6:continue
                    source=task.get('name') if task.get('domain')==domain else domain
                    markup='';offers=[]
                    if pages<240 and domain_pages[domain]<12 and time.monotonic()-started<35*60:
                        pages+=1;domain_pages[domain]+=1
                        try:
                            markup=fetch(url);live[domain]['pagesOpened']+=1
                            offers=page_offers(markup,url,source,profile)
                            if not offers and '/products/' in url:
                                try:
                                    offer=shopify_offer(url,source,profile,fetch)
                                    if offer: offers=[offer]
                                except Exception: pass
                        except urllib.error.HTTPError as error:
                            if error.code in (404,410):
                                live[domain]['pagesOpened']+=1;live[domain]['staleResults']+=1;continue
                        except Exception: pass
                    if len(offers)==1 and offers[0]['searchScore']>=55:
                        c=offers[0];c['verificationState']='verified'
                        if old:c['id']=old['id']
                        link_physical_horn(c,baseline);market_context(c,baseline)
                        live[domain]['verifiedOffers']+=1
                    else:
                        c=candidate_hint(result,source,profile,markup)
                        if old and old.get('verificationState')!='candidate':c=None
                        if not c:continue
                    c['url']=canonical;report['listings'].append(c);submitted.add(canonical);domain_finds[domain]+=1;check['candidates']+=1
                    # The registry grows only from useful offer/lead evidence,
                    # not every arbitrary URL returned by a search engine.
                    live[domain]
                check['note']=f"Search query reviewed; {check['candidates']} offers/leads retained. Search indexing does not establish live availability."
            except Exception as error:
                check.update(status='failed',note='Search provider failed: '+type(error).__name__);failures.append(check['source']+' search failed')
            report['sources'].append(check);print(json.dumps({'source':check['source'],'status':check['status'],'candidates':check['candidates']}),flush=True)
    if pages>=240:failures.append('Discovery page budget exhausted; promising unverified leads retained')
    # Attribute live evidence once per actual domain; query success is separate.
    by_domain={s['domain']:s for s in report['sources'] if s.get('domain')}
    for domain,counts in live.items():
        if domain not in by_domain:
            if not domain_finds[domain] and domain not in known_domains: continue
            check=dict(source='Live pages / '+domain,domain=domain,status='checked' if counts['pagesOpened'] else 'skipped',candidates=domain_finds[domain],note='Direct listing pages reviewed; stock evidence is stored per offer.' if counts['pagesOpened'] else 'Unverified search lead discovered; no live page accessed.',**counts)
            report['sources'].append(check)
        else:by_domain[domain].update(counts)
    if failures:report.update(status='partial',error='; '.join(dict.fromkeys(failures)))
    result=client.call('/runs',report);print(json.dumps(result),flush=True)
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
