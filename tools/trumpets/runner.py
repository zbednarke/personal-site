#!/usr/bin/env python3
"""Daily search/revalidation and ChatGPT report bridge. Python stdlib only.
Secrets are environment variables; feedback is never logged or cached.
"""
import argparse
import base64
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
# Accessories and parts sold under instrument makers' names are not horns, but a
# horn whose title lists what comes with it ("Bb trumpet, mod kit, 5mm
# mouthpiece", "trumpet with extra leadpipe") is still a horn.
ACCESSORY = (r'mouthpieces?|mutes?|stands?|valve oil|cleaning kit|trim kit|t-shirts?|case only'
             r'|backbores?|first aid|lead ?pipes?|valve (?:caps?|tops?|buttons?|guides?)|finger buttons?|trumpet tops?')
# Other instruments named in the item's own phrase are never a Bb trumpet.
OTHER_INSTRUMENTS = r'cornets?|flugelhorns?|trombones?|piccolo'
# "Taylor trumpet case" is a case, "trumpet with original hard case" is a horn.
CASE_PHRASE = r'\b(?:trumpet\s+)?(?:(?:gig|hard|soft|double|single|flight|carrying)\s+)?(?:cases?|bags?)\b'
HORN_WORD = r'\btrumpets?\b|\btrompette\b|\btrompete\b|\btrompet\b|トランペット'
# What follows these introduces included extras, not the item itself.
INCLUDED = r'\s*(?:,|;|\(|\+|&|\bw/|\bwith\b|\bplus\b|\band\b|\bin\b|\bfor\b|\bfits\b|\bincl(?:udes?|uding|\.)?|\s[-–—|]\s)\s*'


ONLY_ACCESSORY = r'\b(?:cases?|bags?|gig ?bags?|mouthpieces?|lead ?pipes?|valves?|valve (?:caps?|tops?|buttons?)|backbores?|mutes?|stands?|parts?|body|bell)\s*(?:\)\s*)?only\b'


def is_accessory(subject, extra=''):
    """True only when the item itself is the accessory: "(case only)", a title
    that starts with the accessory ("trumpet mouthpiece ..."), no horn word left
    once accessory phrases are removed, or an accessory as the head noun of the
    item's own phrase (the part before "with", ",", "+", "includes"...)."""
    subject=subject.lower()
    if re.search(ONLY_ACCESSORY,subject): return True
    head=re.split(INCLUDED,subject,maxsplit=1)[0]
    if re.search(r'\b(?:'+OTHER_INSTRUMENTS+(extra and '|'+extra)+r')\b',head): return True
    accessory=r'\b(?:'+ACCESSORY+r')\b|'+CASE_PHRASE
    if not re.search(accessory,subject): return False
    # "Trumpet mouthpiece Monette B2 ..." / "Mouthpiece for ...": the accessory
    # is the first noun of the item itself.
    if re.match(r'\s*(?:(?:bb|b-flat|b♭)\s+)?(?:(?:'+HORN_WORD+r')\s+)?(?:'+ACCESSORY+r')\b',head): return True
    if not re.search(HORN_WORD,re.sub(accessory,' ',subject)): return True
    horns=[m.end() for m in re.finditer(HORN_WORD,head)]
    parts=[m.start() for m in re.finditer(accessory,head)]
    # "Taylor trumpet mouthpiece": the accessory follows the horn word.
    return bool(parts) and (not horns or max(parts)>=max(horns))


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
    if 'ebay.' in (p.hostname or ''):
        return ebay_id(url)
    if (p.hostname or '').endswith('reverb.com'):
        return reverb_id(url)
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
    if is_accessory(subject): return None
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
    # Horn-inspiration board: makers and tags the owner saved (no URLs/images); bounded.
    inspiration=profile.get('inspirations',[])
    inspired_tags={str(t).lower() for i in inspiration for t in (i.get('tags') or [])}
    inspired_makers={str(i.get('maker','')).lower() for i in inspiration if i.get('maker')}
    bonus+=min(6,2*sum(1 for t in inspired_tags if t and t in text)+(3 if maker.lower() in inspired_makers else 0))
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
    if serial.strip().lower() in ('unknown','none','not','n/a','na','not provided','not available','undisclosed','tbd','0'):serial=''
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
        super().__init__(); self.meta={}; self.title=[]; self.in_title=False; self.micro={}; self.product_scopes=0
    def handle_starttag(self, tag, attrs):
        a=dict(attrs)
        if tag=='title': self.in_title=True
        if tag=='meta':
            key=a.get('property') or a.get('name') or a.get('itemprop')
            if key and a.get('content'): self.meta.setdefault(key.lower(),[]).append(a['content'])
        if re.search(r'schema\.org/(?:Product|IndividualProduct)\b',a.get('itemtype') or ''): self.product_scopes+=1
        # schema.org microdata, e.g. <span itemprop="price" content="2400"> or
        # <link itemprop="availability" href="https://schema.org/InStock">.
        value=a.get('content') or (a.get('href') if tag=='link' else None)
        if a.get('itemprop') and value: self.micro.setdefault(a['itemprop'].lower(),[]).append(value)
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
    def microdata(self):
        """One Product scope with exactly one price, currency and availability."""
        if self.product_scopes!=1: return None
        facts={k:self.micro.get(k,[]) for k in ('price','pricecurrency','availability')}
        if any(len(v)!=1 for v in facts.values()): return None
        return {k:v[0] for k,v in facts.items()}


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
    if c: return [dict(c,evidence='Individual dealer product metadata / explicit availability')]
    micro=metadata.microdata()
    if micro:
        p['offers']={'price':micro['price'],'priceCurrency':micro['pricecurrency'],'availability':micro['availability']}
        c=normalize_product(p,url,source,profile)
        if c: return [dict(c,evidence='schema.org product microdata / explicit availability')]
    return []


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


RETRY_CODES=(429,500,502,503,504)


def api_json(url, headers=None, data=None, get=None, attempts=3, sleep=None):
    """GET/POST a JSON API with bounded backoff on rate limits and server errors.
    Retry-After is honoured up to 30 seconds. Other HTTP errors are raised."""
    get=get or fetch;sleep=sleep or time.sleep
    for attempt in range(attempts):
        try:
            body=get(url,dict(headers or {}),data) if data is not None else get(url,dict(headers or {}))
            return json.loads(body)
        except urllib.error.HTTPError as error:
            if error.code not in RETRY_CODES or attempt==attempts-1: raise
            retry=(error.headers or {}).get('Retry-After') if error.headers else None
            sleep(min(30,int(retry)) if retry and str(retry).isdigit() else 2*(attempt+1))


# ---- Reverb: public listings API (item pages block the VM; the API does not) ----
REVERB_API='https://api.reverb.com/api/listings/'
REVERB_SEARCH='https://api.reverb.com/api/listings'
REVERB_HEADERS={'Accept':'application/hal+json','Accept-Version':'3.0'}
# Band and Orchestra / Brass / Trumpets.
REVERB_TRUMPETS='d1c7768e-068d-4b0f-8e3d-b4e7be71a751'
# Reverb listing states: live is for sale; sold/ordered went to a buyer;
# ended/suspended were withdrawn. Anything else (draft, unknown) stays unverified.
REVERB_STATES={'live':'active','sold':'sold','ordered':'sold','ended':'removed','suspended':'removed'}


REVERB_DOMAINS=('reverb.com',)
# eBay's registrable domains; lookalikes such as ebay.example.org never match.
EBAY_DOMAINS=('ebay.com','ebay.co.uk','ebay.de','ebay.fr','ebay.it','ebay.es','ebay.ca','ebay.com.au','ebay.ie','ebay.at','ebay.ch','ebay.nl','ebay.be','ebay.pl')


def host_in(url, domains):
    """True when the URL's host is one of domains or a subdomain of one."""
    host=(urllib.parse.urlsplit(url or '').hostname or '').lower().rstrip('.')
    return any(host==d or host.endswith('.'+d) for d in domains)


def reverb_id(url):
    """The item id of any reverb.com/item/<id>-slug URL, including app links
    (applink.reverb.com, m.reverb.com) and links wrapped in a query parameter."""
    p=urllib.parse.urlsplit(url or '')
    if p.scheme not in ('https','http') or not host_in(url,REVERB_DOMAINS): return ''
    match=re.search(r'/item/([0-9]+)',p.path)
    if match: return match[1]
    for values in urllib.parse.parse_qs(p.query).values():
        for value in values:
            if 'reverb.com/item/' in value and value!=url:
                found=reverb_id(value)
                if found: return found
    return ''


def is_reverb(url):
    return bool(reverb_id(url))


def reverb_item(item, get_api=None, sleep=None):
    """GET /api/listings/{id} in the listing's own currency (the API converts to
    a display currency guessed from the caller; a mismatch is refetched with
    X-Display-Currency)."""
    data=api_json(REVERB_API+item,REVERB_HEADERS,get=get_api,sleep=sleep)
    native=str(data.get('listing_currency') or '').upper()
    shown=str((data.get('price') or {}).get('currency') or '').upper()
    if re.fullmatch('[A-Z]{3}',native) and shown and shown!=native:
        data=api_json(REVERB_API+item,dict(REVERB_HEADERS,**{'X-Display-Currency':native}),get=get_api,sleep=sleep)
    return data


def reverb_listing(url, get_api=None, sleep=None):
    item=reverb_id(url)
    return reverb_item(item,get_api,sleep) if item else None


def reverb_search(term, get_api=None, price_min=800, per_page=24, sleep=None):
    """Newest trumpets matching a maker/trait term (empty term = all newest)."""
    params={'category_uuid':REVERB_TRUMPETS,'price_min':price_min,'sort':'published_at|desc','per_page':per_page}
    if term: params['query']=term
    return api_json(REVERB_SEARCH+'?'+urllib.parse.urlencode(params),REVERB_HEADERS,get=get_api,sleep=sleep).get('listings') or []


def reverb_facts(data):
    """State, price, currency, shipping and post date from Reverb's API JSON."""
    state=str((data.get('state') or {}).get('slug') or '').lower()
    status=REVERB_STATES.get(state)
    if status is None: return None
    price_info=data.get('price') or {}
    currency=str(price_info.get('currency') or '').upper()
    if not re.fullmatch('[A-Z]{3}',currency): return None
    native=str(data.get('listing_currency') or currency).upper()
    if native!=currency: return None  # never store a converted display price
    price=amount(price_info.get('amount'))
    shipping=None
    for rate in (data.get('shipping') or {}).get('rates') or []:
        value=(rate or {}).get('rate') or {}
        if rate.get('region_code')=='US' and str(value.get('currency','')).upper()==currency:
            shipping=amount(value.get('amount'))
    posted=None
    if isinstance(data.get('published_at'),str):
        try:posted=dt.datetime.fromisoformat(data['published_at'].replace('Z','+00:00')).astimezone(dt.timezone.utc).isoformat()
        except ValueError:pass
    images=[]
    for photo in data.get('photos') or []:
        href=((photo or {}).get('_links') or {}).get('full',{}).get('href','')
        if isinstance(href,str) and href.startswith('https://'):images.append(href)
    location=(data.get('location') or {}).get('display_location','') if isinstance(data.get('location'),dict) else ''
    return dict(state=state,status=status,price=price,currency=currency,shipping=shipping,postedAt=posted,images=images[:20],
                seller=str(data.get('shop_name') or ''),location=str(location or ''),sourceListingId=str(data.get('id') or ''))


def reverb_web_url(data, fallback=''):
    href=((data.get('_links') or {}).get('web') or {}).get('href','')
    return canonical_url(href) if reverb_id(href) else fallback


def reverb_offer_from(data, url, source, profile):
    facts=reverb_facts(data)
    if not facts or facts['status'] not in ('active','sold'): return None
    p={'name':str(data.get('title') or ''),'description':str(data.get('description') or ''),'brand':{'name':str(data.get('make') or '')},
       'model':str(data.get('model') or '') or None,'productID':facts['sourceListingId'],'image':facts['images'],'datePublished':data.get('published_at'),
       'offers':{'price':facts['price'],'priceCurrency':facts['currency'],'availability':'InStock' if facts['status']=='active' else 'SoldOut','seller':{'name':facts['seller']}}}
    c=normalize_product(p,url,source,profile)
    if not c: return None
    c.update(shipping=facts['shipping'],location=facts['location'],postedAt=facts['postedAt'],evidence='Reverb public listings API / state '+facts['state'])
    return c


def reverb_offer(url, source, profile, get_api=None):
    """A verified offer from Reverb's public listings API, or None."""
    data=reverb_listing(url,get_api)
    if data is None: return None
    # App links resolve to the item's web URL; reverb.com URLs keep their identity.
    host=(urllib.parse.urlsplit(url).hostname or '').lower().removeprefix('www.')
    return reverb_offer_from(data,url if host=='reverb.com' else reverb_web_url(data,url),source,profile)


# ---- eBay: official Browse API with an application (client-credentials) token ----
EBAY_TOKEN_URL='https://api.ebay.com/identity/v1/oauth2/token'
EBAY_BROWSE='https://api.ebay.com/buy/browse/v1'
EBAY_SCOPE='https://api.ebay.com/oauth/api_scope'
# Musical Instruments & Gear / Brass / Trumpets (override with EBAY_TRUMPET_CATEGORY_ID).
EBAY_TRUMPETS='16214'


def ebay_id(url):
    p=urllib.parse.urlsplit(url or '')
    if p.scheme not in ('https','http') or not host_in(url,EBAY_DOMAINS): return ''
    match=re.search(r'/itm/(?:[^/]+/)?([0-9]{9,15})(?:[/?]|$)',p.path+'/')
    return match[1] if match else ''


class EbayAPI:
    """Browse API client. The token is cached in memory until shortly before it
    expires and is never logged or written anywhere."""
    def __init__(self, client_id, client_secret, get=None, clock=None, sleep=None, marketplace='EBAY_US'):
        self._credentials=base64.b64encode(f'{client_id}:{client_secret}'.encode()).decode()
        self.get=get;self.clock=clock or time.monotonic;self.sleep=sleep;self.marketplace=marketplace
        self._token=None;self._expires=0;self.category=os.environ.get('EBAY_TRUMPET_CATEGORY_ID',EBAY_TRUMPETS)
    @classmethod
    def from_env(cls, **kw):
        client_id=os.environ.get('EBAY_CLIENT_ID','').strip();secret=os.environ.get('EBAY_CLIENT_SECRET','').strip()
        return cls(client_id,secret,**kw) if client_id and secret else None
    def __repr__(self): return 'EbayAPI(<credentials hidden>)'
    def token(self):
        if self._token and self.clock()<self._expires: return self._token
        body=urllib.parse.urlencode({'grant_type':'client_credentials','scope':EBAY_SCOPE}).encode()
        payload=api_json(EBAY_TOKEN_URL,{'Authorization':'Basic '+self._credentials,'Content-Type':'application/x-www-form-urlencoded','Accept':'application/json'},body,self.get,sleep=self.sleep)
        self._token=payload['access_token']
        self._expires=self.clock()+max(60,int(payload.get('expires_in') or 7200)-120)
        return self._token
    def call(self, path):
        for attempt in range(2):
            headers={'Authorization':'Bearer '+self.token(),'X-EBAY-C-MARKETPLACE-ID':self.marketplace,'Accept':'application/json'}
            try: return api_json(EBAY_BROWSE+path,headers,get=self.get,sleep=self.sleep)
            except urllib.error.HTTPError as error:
                if error.code!=401 or attempt: raise
                self._token=None  # expired early: fetch one new token, once
    def item(self, legacy_id):
        return self.call('/item/get_item_by_legacy_id?'+urllib.parse.urlencode({'legacy_item_id':legacy_id}))
    def search(self, term, price_min=800, price_max=30000, limit=25):
        params={'q':(term+' trumpet').strip(),'category_ids':self.category,'sort':'newlyListed','limit':limit,
                'filter':f'buyingOptions:{{FIXED_PRICE|AUCTION|BEST_OFFER}},price:[{price_min}..{price_max}],priceCurrency:USD'}
        return self.call('/item_summary/search?'+urllib.parse.urlencode(params)).get('itemSummaries') or []


def ebay_money(value):
    if not isinstance(value,dict): return None,''
    return amount(value.get('value')),str(value.get('currency') or '').upper()


def ebay_facts(item, now=None):
    """Price, availability, end date, condition, location, seller and images."""
    now=now or dt.datetime.now(dt.timezone.utc)
    options=item.get('buyingOptions') or []
    price,currency=ebay_money(item.get('price'))
    if 'AUCTION' in options and item.get('currentBidPrice'): price,currency=ebay_money(item['currentBidPrice'])
    if not re.fullmatch('[A-Z]{3}',currency): return None
    ends=None
    if isinstance(item.get('itemEndDate'),str):
        try:ends=dt.datetime.fromisoformat(item['itemEndDate'].replace('Z','+00:00')).astimezone(dt.timezone.utc)
        except ValueError:pass
    availability={str(a.get('estimatedAvailabilityStatus','')).upper() for a in item.get('estimatedAvailabilities') or [] if isinstance(a,dict)}
    status='active'
    if availability and availability<={'OUT_OF_STOCK'}: status='sold'
    if ends and ends<now and status=='active': status='removed'
    shipping=None
    for option in item.get('shippingOptions') or []:
        cost,cost_currency=ebay_money((option or {}).get('shippingCost'))
        if cost is not None and cost_currency==currency: shipping=cost;break
    posted=None
    if isinstance(item.get('itemCreationDate'),str):
        try:posted=dt.datetime.fromisoformat(item['itemCreationDate'].replace('Z','+00:00')).astimezone(dt.timezone.utc).isoformat()
        except ValueError:pass
    images=[i.get('imageUrl','') for i in [item.get('image') or {}]+list(item.get('additionalImages') or []) if isinstance(i,dict)]
    location=item.get('itemLocation') or {}
    place=', '.join(str(location[k]) for k in ('city','stateOrProvince','country') if isinstance(location.get(k),str) and location.get(k))
    return dict(status=status,price=price,currency=currency,shipping=shipping,postedAt=posted,endsAt=ends.isoformat() if ends else None,
                condition=str(item.get('condition') or ''),location=place,seller=str((item.get('seller') or {}).get('username') or ''),
                images=[i for i in images if i.startswith('https://')][:20],sourceListingId=str(item.get('legacyItemId') or ''),auction='AUCTION' in options)


def ebay_offer_from(item, url, source, profile):
    facts=ebay_facts(item)
    if not facts or facts['status'] not in ('active','sold'): return None
    p={'name':str(item.get('title') or ''),'description':str(item.get('shortDescription') or item.get('description') or ''),'brand':{'name':str(item.get('brand') or '')},
       'productID':facts['sourceListingId'],'image':facts['images'],'datePublished':item.get('itemCreationDate'),'itemCondition':facts['condition'],
       'offers':{'price':facts['price'],'priceCurrency':facts['currency'],'availability':'InStock' if facts['status']=='active' else 'SoldOut','seller':{'name':facts['seller']}}}
    c=normalize_product(p,url,source,profile)
    if not c: return None
    evidence='eBay Browse API'+(' / auction, current bid' if facts['auction'] else '')+(' / ends '+facts['endsAt'][:10] if facts['endsAt'] else '')
    c.update(shipping=facts['shipping'],location=facts['location'],postedAt=facts['postedAt'],evidence=evidence)
    if facts['condition']:c['details']={**c.get('details',{}),'condition':facts['condition']}
    if facts['auction']:c['tags']=list(dict.fromkeys(c.get('tags',[])+['auction']))
    return c


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
    if not maker or is_accessory(subject,'c trumpet|eb trumpet|d trumpet'): return None
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


def apply_api_facts(c, facts, offer, evidence):
    """Update a tracked offer from official-API facts (Reverb, eBay)."""
    c['verifiedVia']='api'
    if c.get('verificationState')=='candidate':
        if facts['status']=='removed':
            c.update(status='removed',evidence=evidence)
            return c
        return dict(offer,id=c.get('id'),verificationState='verified',verifiedVia='api') if offer else None
    c.update(status=facts['status'],evidence=evidence)
    if facts['price'] is not None:
        if facts['currency']!=c.get('currency'):c['shipping']=None
        c.update(price=facts['price'],currency=facts['currency'])
    if facts['shipping'] is not None and facts['currency']==c.get('currency'):c['shipping']=facts['shipping']
    for key in ('postedAt','seller','location'):
        if facts[key]:c[key]=facts[key]
    if facts['sourceListingId']:c['sourceListingId']=facts['sourceListingId']
    if facts['images']:c['images']=facts['images']
    if offer:
        c.update(searchScore=offer['searchScore'],searchRationale=offer['searchRationale'],tags=offer['tags'])
        c['details']={**c.get('details',{}),**{k:v for k,v in offer.get('details',{}).items() if v}}
        if offer.get('description'):c['description']=offer['description']
    return c


def api_gone(c, label, code):
    c['verifiedVia']='api'
    if c.get('verificationState')=='candidate':
        c.update(status='removed',price=None,shipping=None,evidence=f'{label} HTTP {code}; search lead no longer exists')
    else:
        c.update(status='removed',evidence=f'{label} HTTP {code}; prior price retained as last known')
    return c


def recheck_reverb(c, profile, get_api=None):
    """Recheck a Reverb item through the public API: state, price, currency,
    shipping and post date. HTTP 404/410 from the API means the item is gone."""
    try: data=reverb_listing(c['url'],get_api)
    except urllib.error.HTTPError as error:
        if error.code not in (404,410): raise
        return api_gone(c,'Reverb API',error.code)
    facts=reverb_facts(data) if data else None
    if not facts: return None
    return apply_api_facts(c,facts,reverb_offer_from(data,c['url'],c['source'],profile),'Reverb public listings API / state '+facts['state'])


def recheck_ebay(c, profile, ebay):
    """Recheck an eBay item through the Browse API (getItemByLegacyId). The API
    only returns live items: 404 means the listing ended or sold."""
    try: item=ebay.item(ebay_id(c['url']))
    except urllib.error.HTTPError as error:
        if error.code not in (404,410): raise
        return api_gone(c,'eBay Browse API',error.code)
    facts=ebay_facts(item)
    if not facts: return None
    offer=ebay_offer_from(item,c['url'],c['source'],profile)
    return apply_api_facts(c,facts,offer,offer['evidence'] if offer else 'eBay Browse API')


class ChallengePage(Exception):
    """A bot-challenge / interstitial page instead of the listing."""


CHALLENGE_TITLE=re.compile(r'just a moment|attention required|access denied|pardon our interruption|are you a robot|security check|verify you are (?:a )?human|captcha',re.I)
CHALLENGE_MARKUP=re.compile(r'cf-chl-|px-captcha|_Incapsula_Resource|captcha-delivery\.com|/sttc/px/',re.I)


def is_challenge(page):
    if products(page): return False
    title=re.search(r'<title[^>]*>(.*?)</title>',page[:200000],re.I|re.S)
    return bool((title and CHALLENGE_TITLE.search(title[1])) or CHALLENGE_MARKUP.search(page[:200000]))


def recheck(listing, get_page, profile, get_api=None, ebay=None):
    """Official API first (Reverb; eBay with keys), then the page itself."""
    c={k:copy.deepcopy(v) for k,v in listing.items() if k in ALLOWED_FIELDS}
    c['discoveryType']='rediscovered'
    if not c.get('url'): return None
    if reverb_id(c['url']): return recheck_reverb(c,profile,get_api)
    if ebay is not None and ebay_id(c['url']): return recheck_ebay(c,profile,ebay)
    result=recheck_page(c,get_page,profile)
    if result is not None: result.setdefault('verifiedVia','direct')
    return result


def recheck_page(c, get_page, profile):
    try: page=get_page(c['url'])
    except urllib.error.HTTPError as error:
        if error.code not in (404,410): raise
        if c.get('verificationState')=='candidate':
            c.update(status='removed',price=None,shipping=None,evidence=f'HTTP {error.code}; search lead page no longer exists')
            return c
        c.update(status='removed',evidence=f'HTTP {error.code}; prior price retained as last known')
        return c
    if is_challenge(page): raise ChallengePage('bot challenge page')
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
    else:
        c['evidence']+=' / current price unpublished; prior price and its original currency retained'
        c['searchRationale']+=' Current asking price unpublished; the displayed amount is last known, not freshly verified.'
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


# Report fields newer than some deployed APIs; a 400 for unknown JSON retries
# once without them so a runner deployed before its API still records runs.
NEW_REPORT_FIELDS=('verifiedVia',)


def submit_run(client, report):
    try: return client.call('/runs',report)
    except urllib.error.HTTPError as error:
        if error.code!=400: raise
        try: body=error.read().decode('utf-8','replace')
        except Exception: body=''
        if 'unknown field' not in body and 'invalid run JSON' not in body: raise
        stripped=dict(report,listings=[{k:v for k,v in l.items() if k not in NEW_REPORT_FIELDS} for l in report.get('listings',[])])
        print(json.dumps({'note':'API predates new report fields; resubmitted without them'}),flush=True)
        return client.call('/runs',stripped)


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


# Explore/exploit: an option's value is its learned weight plus TASTE_KAPPA
# posterior sds (an upper confidence bound), so liked and still-uncertain
# options both earn budget; an option whose weight+sd stays below zero after a
# few reactions has been consistently rejected and is demoted.
TASTE_KAPPA=0.5
TRAIT_PHRASES={'finish:raw brass':'raw brass','finish:raw nickel':'raw nickel','finish:gold plate':'gold plated','finish:silver plate':'silver plated',
    'finish:patina':'patina aged brass','finish:mixed metals':'mixed metals','finish:black':'black finish','finish:copper':'copper bell',
    'bell:upswept':'upswept bell','bell:unusual':'unusual bell','leadpipe:reverse':'reverse leadpipe','leadpipe:interchangeable':'interchangeable leadpipes',
    'trait:engraving':'engraved','trait:one-off':'one-off custom prototype','trait:provenance':'artist owned provenance','trait:heavy':'heavyweight',
    'trait:engineering':'unusual engineering','era:vintage':'vintage','era:classic':'1970s 1980s'}


def norm(text):
    return ' '.join(str(text).lower().split())


class Taste:
    """The server's learned weights and uncertainties (profile['taste']), plus
    the owner's explicit preferences. No notes or listing ids are involved."""
    def __init__(self,profile):
        taste=profile.get('taste') or {}
        self.prior=float(taste.get('priorSd') or 1)
        self.weights={w['feature']:w for w in taste.get('weights') or [] if isinstance(w,dict) and w.get('feature')}
        prefs=profile.get('preferences') or {}
        # Owner-stated preferences; traits arrive resolved to model feature keys.
        self.liked={'maker:'+norm(m) for m in prefs.get('likedMakers') or []}|set(prefs.get('likedTraitKeys') or [])
        self.disliked={'maker:'+norm(m) for m in prefs.get('dislikedMakers') or []}|set(prefs.get('dislikedTraitKeys') or [])
        self.active=bool(self.weights or self.liked or self.disliked)
    def stats(self,key):
        w=self.weights.get(key)
        return (float(w.get('weight') or 0),float(w.get('sd') if w.get('sd') is not None else self.prior),float(w.get('n') or 0)) if w else (0.0,self.prior,0.0)
    def value(self,key):
        weight,sd,_=self.stats(key)
        return weight+TASTE_KAPPA*sd+(0.5 if key in self.liked else 0)-(1.0 if key in self.disliked else 0)
    def rejected(self,key):
        weight,sd,n=self.stats(key)
        return key in self.disliked or (n>=2 and weight+sd<0)
    def favored(self,key):
        weight,_,_=self.stats(key)
        return key in self.liked or weight>0.25
    def makers_in(self,query):
        q=norm(query)
        keys={'maker:'+norm(m) for m in MAKERS if re.search(r'\b'+re.escape(norm(m))+r'\b',q)}
        keys|={k for k,w in self.weights.items() if w.get('kind')=='maker' and norm(w.get('label',''))
               and re.search(r'\b'+re.escape(norm(w['label']))+r'\b',q)}
        return keys
    def query_value(self,query):
        keys=self.makers_in(query)
        return max((self.value(k) for k in keys),default=TASTE_KAPPA*self.prior)
    def query_rejected(self,query):
        keys=self.makers_in(query)
        return bool(keys) and all(self.rejected(k) for k in keys)
    def ranked_queries(self,queries):
        kept=[q for q in queries if not self.query_rejected(q)] or list(queries)
        return sorted(kept,key=lambda q:-self.query_value(q))
    def trait_terms(self):
        traits=sorted((k for k in set(self.weights)|self.liked if k in TRAIT_PHRASES and not self.rejected(k) and self.stats(k)[0]+(0.5 if k in self.liked else 0)>0),
                      key=lambda k:-self.value(k))
        return [TRAIT_PHRASES[k] for k in traits]
    def trait_query(self):
        traits=self.trait_terms()
        if not traits:return None
        return ' '.join(traits[:2])+' professional Bb trumpet used sale'
    def source_key(self,domain):
        domain=domain.lower().removeprefix('www.')
        return 'source:'+('ebay' if domain.startswith('ebay.') else domain)
    def page_cap(self,domain):
        key=self.source_key(domain)
        if not self.active:return 12
        return 6 if self.rejected(key) else 16 if self.favored(key) else 12


def search_plan(profile,day=None):
    day=day or dt.datetime.now(dt.timezone.utc).date()
    taste=Taste(profile)
    universe=profile.get('sourceUniverse') or [dict(domain=d,name=n,specialty='specialist dealer',geography='International') for n,ds in SOURCES.items() for d in ds]
    # Sort never/oldest searched sources first; date-dependent ties avoid repeating
    # the same seed subset. Useful sources get a bounded portion of the run.
    rank=lambda s:hashlib.sha256((day.isoformat()+s['domain']).encode()).hexdigest()
    oldest=sorted(universe,key=lambda s:(s.get('lastSearched') or '',rank(s)))
    useful=sorted((s for s in universe if s.get('verifiedOffers',0)>0),key=rank)[:8]
    favored=[]
    if taste.active:
        # Learned taste: up to six liked/uncertain-but-promising sources first;
        # consistently rejected sources only fill slots nothing else needs.
        key=lambda s:taste.source_key(s['domain'])
        favored=sorted((s for s in universe if taste.favored(key(s)) and not taste.rejected(key(s))),key=lambda s:(-taste.value(key(s)),rank(s)))[:6]
        useful=[s for s in useful if not taste.rejected(key(s))]
        oldest=[s for s in oldest if not taste.rejected(key(s))]+[s for s in oldest if taste.rejected(key(s))]
    selected=[];families=collections.Counter()
    for source in favored+useful+oldest:
        domain=source['domain']
        if any(s['domain']==domain for s in selected): continue
        family='ebay' if domain.startswith('ebay.') else domain
        if families[family]>=1: continue
        families[family]+=1;selected.append(source)
        if len(selected)==36: break
    offset=day.toordinal()
    # Learned taste reweights the maker phrases: the best options appear twice in
    # the rotation, consistently rejected ones drop out. No taste = old rotation.
    ranked=taste.ranked_queries(MAKER_QUERIES) if taste.active else MAKER_QUERIES
    phrases=ranked+ranked[:3] if taste.active else MAKER_QUERIES
    plan=[]
    for i,source in enumerate(selected):
        explore=i%4==0
        phrase=EXPLORATION_QUERIES[(offset+i)%len(EXPLORATION_QUERIES)] if explore else phrases[(offset+i)%len(phrases)]
        task={**source,'source':source['name']+' / '+source['domain'],'query':'site:'+source['domain']+' '+phrase,'exploration':explore}
        if taste.active:task['priority']=(round(taste.value(taste.source_key(source['domain'])),3),round(0 if explore else taste.query_value(phrase),3))
        plan.append(task)
    # Higher-value searches run first, so they get the page and time budgets.
    if taste.active:plan.sort(key=lambda t:(-t['priority'][0],-t['priority'][1]))
    # All known domains are excluded from deliberate source-expansion queries.
    exclusions=''
    for source in universe:
        term=' -site:'+source['domain']
        if len(exclusions)+len(term)>3000: break
        exclusions+=term
    for i in range(4):
        plan.append(dict(source='New source discovery '+str(i+1),domain='',query=NEW_SOURCE_QUERIES[(offset*4+i)%len(NEW_SOURCE_QUERIES)]+' '+exclusions,exploration=True))
    if taste.active:
        # Three best-value maker searches, two rotating (still exploring) and,
        # when the owner likes particular traits, one trait search.
        rest=ranked[3:] or ranked
        makers=ranked[:3]+[rest[(offset+i)%len(rest)] for i in range(3)]
        makers=list(dict.fromkeys(makers))
        trait=taste.trait_query()
        if trait:makers=makers[:5]+[trait]
        for i in range(len(makers),6):makers.append(MAKER_QUERIES[(offset+i)%len(MAKER_QUERIES)])
    else:
        makers=[MAKER_QUERIES[(offset+i)%len(MAKER_QUERIES)] for i in range(6)]
    for i,query in enumerate(makers[:6]):
        plan.append(dict(source='Maker search '+str(i+1),domain='',query=query+' -site:reverb.com',exploration=False))
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


# ---- Search-provider fallback verification ------------------------------------
# When a page blocks direct checks (401/403/429 or a bot challenge) and there is
# no usable official API, one OpenAI Responses call with the web_search tool is
# asked about that exact URL. Its JSON is accepted only when the web-search
# sources include the same listing (canonical URL, or the same eBay/Reverb item
# id) and it matches the schema; generated prose alone is never evidence.
OPENAI_RESPONSES='https://api.openai.com/v1/responses'
VERIFY_LIMIT=60
VERIFY_SCHEMA={'type':'object','additionalProperties':False,'required':['url','title','state','price','currency','shipping','evidence'],
    'properties':{'url':{'type':'string'},'title':{'type':['string','null']},'state':{'type':'string','enum':['available','sold','ended','unknown']},
                  'price':{'type':['number','null']},'currency':{'type':['string','null']},'shipping':{'type':['number','null']},
                  'evidence':{'type':'string'}}}


def same_listing(a, b):
    if not a or not b: return False
    for item in (ebay_id, reverb_id):
        x,y=item(a),item(b)
        if x or y: return bool(x) and x==y
    try: return canonical_url(a)==canonical_url(b)
    except ValueError: return False


def valid_verification(data):
    if not isinstance(data,dict) or set(data)!=set(VERIFY_SCHEMA['required']): return False
    if data['state'] not in ('available','sold','ended','unknown') or not isinstance(data['url'],str) or not isinstance(data['evidence'],str): return False
    if data['title'] is not None and (not isinstance(data['title'],str) or len(data['title'])>500): return False
    for key in ('price','shipping'):
        v=data[key]
        if v is not None and (isinstance(v,bool) or not isinstance(v,(int,float)) or not 0<=v<=999999999999.99): return False
    return data['currency'] is None or (isinstance(data['currency'],str) and bool(re.fullmatch('[A-Z]{3}',data['currency'].upper())))


def verify_via_search(url, key, get=None, sleep=None):
    """Ask the search provider about one listing URL. Returns the parsed JSON
    when a web-search source is the same listing, else None."""
    get=get or (lambda u,h,d=None:fetch(u,h,d,timeout=120))
    model=os.environ.get('TRUMPETS_VERIFY_MODEL') or os.environ.get('TRUMPETS_SEARCH_MODEL','gpt-4.1-mini')
    request={'model':model,'tools':[{'type':'web_search','search_context_size':'medium'}],'tool_choice':'required',
             'include':['web_search_call.action.sources'],'max_output_tokens':800,
             'text':{'format':{'type':'json_schema','name':'listing_state','strict':True,'schema':VERIFY_SCHEMA}},
             'input':'Look up this exact used-instrument listing page and report its current state as JSON. '
                     'state is "available" only if the page offers it for sale now, "sold" if it says sold, "ended" if the listing ended or was removed, '
                     'otherwise "unknown". Give the listing title, asking price, ISO currency and shipping exactly as the page shows them, or null. '
                     'Do not guess or use other listings. URL: '+url}
    payload=api_json(OPENAI_RESPONSES,{'Authorization':'Bearer '+key,'Content-Type':'application/json'},json.dumps(request).encode(),get,sleep=sleep)
    if payload.get('status')!='completed': return None
    sources=[];citations=[];text=''
    for item in payload.get('output',[]):
        if item.get('type')=='web_search_call' and item.get('status')=='completed':
            action=item.get('action') or {}
            # Only search results count: an open_page/find_in_page of the URL we
            # supplied proves nothing (the fetch itself may have been blocked).
            if action.get('type')=='search':
                sources+=[s.get('url','') for s in action.get('sources') or [] if isinstance(s,dict)]
        if item.get('type')=='message':
            for content in item.get('content',[]):
                if content.get('type')!='output_text':continue
                text+=content.get('text','')
                citations+=[a.get('url','') for a in content.get('annotations') or [] if isinstance(a,dict) and a.get('type')=='url_citation']
    # The listing must be among the search results AND cited by the answer.
    if not any(same_listing(s,url) for s in sources) or not any(same_listing(c,url) for c in citations): return None
    try: data=json.loads(text)
    except ValueError: return None
    return data if valid_verification(data) else None


def unconfirmed_marker(state):
    return f'search provider reports {state} (unconfirmed)'


def apply_search_verification(listing, data, today=None):
    """Returns (checked listing or None, failure reason or None).

    "available" confirms an already verified offer is still for sale (and a
    stated price). A single "sold"/"ended" never retires a verified offer: it is
    recorded as a failed check, and only the same report on a later UTC day
    retires it. Prices only when the page states them with a currency."""
    if not data or data['state']=='unknown': return None,None
    today=today or dt.datetime.now(dt.timezone.utc).date().isoformat()
    state=data['state']
    if state in ('sold','ended'):
        earlier=str(listing.get('lastCheckAttempt') or '')[:10]
        if unconfirmed_marker(state) not in str(listing.get('lastCheckError') or '') or not earlier or earlier>=today:
            return None,unconfirmed_marker(state)
    c={k:copy.deepcopy(v) for k,v in listing.items() if k in ALLOWED_FIELDS}
    c.update(discoveryType='rediscovered',verifiedVia='search-provider',
             status={'available':'active','sold':'sold','ended':'removed'}[state],
             evidence='Search-provider verification (web search source matched this listing) / '+state+(' on two separate days' if state!='available' else ''))
    currency=(data['currency'] or '').upper()
    if data['state']=='available' and data['price'] is not None and currency:
        if currency!=c.get('currency'):c['shipping']=None
        c.update(price=round(float(data['price']),2),currency=currency)
        if data['shipping'] is not None:c['shipping']=round(float(data['shipping']),2)
    return c,None


# ---- Marketplace discovery through official APIs -------------------------------
MARKET_MAKERS=['Taylor','Harrelson','AR Resonance','Monette','Adams','Blackburn','Van Laar','Schilke','Inderbinen','Lawler','LOTUS','BAC','Eclipse','Del Quadro','Calicchio','Benge','GERDT','Olds']


def marketplace_terms(taste, day):
    """Maker/trait terms for Reverb and eBay searches: the taste model's best
    (and most uncertain) makers, two rotating makers, liked traits, and one
    query for everything newest."""
    n=len(MARKET_MAKERS);offset=day.toordinal()%n
    rotation=MARKET_MAKERS[offset:]+MARKET_MAKERS[:offset]
    if taste.active:
        kept=[m for m in rotation if not taste.rejected('maker:'+norm(m))] or rotation
        best=sorted(kept,key=lambda m:-taste.value('maker:'+norm(m)))[:3]
        makers=best+[m for m in kept if m not in best][:2]
    else:
        makers=rotation[:5]
    traits=taste.trait_terms()
    return makers+([' '.join(traits[:2])] if traits else [])+['']


BLOCKING_CODES = (401, 403, 429)
LEAD_CHECKS_PER_RUN = 40


def failure_reason(error):
    """A short, non-sensitive reason for a failed page check."""
    if isinstance(error,urllib.error.HTTPError):
        return f'HTTP {error.code}'+(' (source blocks automated checks)' if error.code in BLOCKING_CODES else '')
    if isinstance(error,(TimeoutError,socket.timeout)): return 'timed out'
    if isinstance(error,urllib.error.URLError): return 'network error ('+type(error.reason).__name__+')'
    return type(error).__name__


def recheck_order(due):
    """Verified offers first, then leads least-recently attempted first.

    lastCheckAttempt is the latest recorded failed attempt; never-attempted leads
    go first (newest first), so every lead is retried in rotation rather than
    only the newest 40 forever.
    """
    verified=[l for l in due if l.get('verificationState')!='candidate']
    # Active, high-score offers first: they get capped fallback checks first.
    verified.sort(key=lambda l:(l.get('status')!='active',-(l.get('searchScore') or 0)))
    leads=[l for l in due if l.get('verificationState')=='candidate']
    leads.sort(key=lambda l:(l.get('firstSeen') or '',l.get('id') or ''),reverse=True)
    leads.sort(key=lambda l:l.get('lastCheckAttempt') or '')
    return verified+leads


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
    taste=Taste(profile)
    # Daily due is also useful to external clients; each actual watch invocation
    # revalidates all active offers, including manual reruns on the same UTC day.
    due_by_id={l['id']:l for l in due}
    for listing in baseline:
        if listing['status']=='active' and not listing.get('acquired'):due_by_id[listing['id']]=listing
    due=list(due_by_id.values())
    by_url={canonical_url(l['url']):l for l in baseline if l.get('url')}
    report={'externalId':'adaptive-watch-'+dt.datetime.now(dt.timezone.utc).isoformat(),'startedAt':dt.datetime.now(dt.timezone.utc).isoformat(),
            'kind':'combined','status':'succeeded','sources':[],'listings':[],'checkFailures':[],'error':''}
    # failures make the run partial (coverage really missing); notes explain
    # bounded or recorded outcomes without hiding them.
    failures=[];notes=[];submitted=set();started=time.monotonic();candidate_checks=0;pages=0;domain_pages=collections.Counter();domain_finds=collections.Counter()
    live=collections.defaultdict(lambda:dict(pagesOpened=0,verifiedOffers=0,staleResults=0))
    key=os.environ.get('BRAVE_SEARCH_API_KEY');openai=os.environ.get('OPENAI_API_KEY')
    blocked=set();leads=collections.Counter();ebay_results=[]
    ebay=EbayAPI.from_env()
    verify_limit=int(os.environ.get('TRUMPETS_VERIFY_LIMIT') or VERIFY_LIMIT);verifier=collections.Counter()
    def search_provider_check(listing,domain):
        """Fallback after a blocked direct check: (verified, failure reason)."""
        if not openai or listing.get('verificationState')=='candidate': return False,None
        if verifier['calls']>=verify_limit or time.monotonic()-started>35*60: return False,'search-provider check budget reached'
        verifier['calls']+=1
        try: checked,reason=apply_search_verification(listing,verify_via_search(listing['url'],openai))
        except Exception: checked,reason=None,None
        if not checked:
            verifier['rejected']+=1;return False,reason or 'search-provider check found no matching cited source'
        verifier['accepted']+=1;live[domain]['pagesOpened']+=1;live[domain]['verifiedOffers']+=1
        if checked['status']=='removed':live[domain]['staleResults']+=1
        link_physical_horn(checked,baseline);report['listings'].append(checked);submitted.add(canonical_url(listing['url']))
        return True,None
    def record_failure(listing,domain,reason):
        report['checkFailures'].append({'id':listing['id'],'reason':(domain+' / '+reason)[:500]})
        if listing.get('verificationState')!='candidate':
            notes.append('Recheck failed, recorded for retry: '+domain+' / '+reason)
    # Direct revalidation of every active offer takes precedence over discovery.
    # A failed check is recorded per listing (the server counts consecutive
    # failed days and marks a persistently unreachable offer unverifiable)
    # instead of pinning every run to partial.
    for listing in recheck_order(due):
        if not listing['url']: continue
        domain=urllib.parse.urlsplit(listing['url']).hostname.removeprefix('www.')
        lead=listing.get('verificationState')=='candidate'
        if lead and ebay is None and ebay_id(listing['url']):
            # eBay pages refuse the VM and search-provider answers never promote a
            # lead, so eBay leads wait (without using a lead slot) for review.
            leads['ebay']+=1;continue
        if lead:
            if candidate_checks>=LEAD_CHECKS_PER_RUN or time.monotonic()-started>35*60: continue
            if domain in blocked:
                # Do not spend a lead slot on a domain that already refused this run.
                record_failure(listing,domain,'skipped; source blocked automated checks earlier in this run');leads['skipped']+=1;continue
            candidate_checks+=1
        if ebay is None and ebay_id(listing['url']) and openai:
            # No eBay developer keys: eBay pages always refuse the VM, so the
            # source-matched search-provider check is the primary path.
            live[domain]
            ok,reason=search_provider_check(listing,domain)
            if not ok:record_failure(listing,domain,'eBay: '+(reason or 'search-provider check unavailable'))
            continue
        try:
            checked=recheck(listing,fetch,profile,ebay=ebay)
            live[domain]['pagesOpened']+=1
            if checked:
                link_physical_horn(checked,baseline);market_context(checked,baseline);report['listings'].append(checked);submitted.add(canonical_url(listing['url']))
                live[domain]['verifiedOffers']+=1
                if checked['status']=='removed':live[domain]['staleResults']+=1;leads['retired']+=lead
                elif lead:leads['verified']+=1
            else:
                record_failure(listing,domain,'page did not expose a single verifiable offer (price, currency and stock)');leads['unverified']+=lead
        except Exception as error:
            refused=isinstance(error,ChallengePage) or (isinstance(error,urllib.error.HTTPError) and error.code in BLOCKING_CODES)
            if refused:blocked.add(domain)
            # Official API, then direct page, then the search-provider fallback.
            ok,reason=search_provider_check(listing,domain) if refused else (False,None)
            if ok:continue
            record_failure(listing,domain,failure_reason(error)+(' / '+reason if reason else ''));leads['failed']+=lead
    if candidate_checks or leads['skipped']:
        notes.append(f"Lead rotation: {candidate_checks} queued leads checked ({leads['verified']} verified, {leads['retired']} retired as gone, {leads['unverified']} still unverified, {leads['failed']} unreachable), {leads['skipped']} skipped on blocked domains")
    if leads['ebay']:
        notes.append(f"{leads['ebay']} eBay leads await review (no eBay API keys; search-provider answers never promote a lead)")
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
                    if canonical in submitted or (old and (old.get('acquired') or old.get('feedback',{}).get('interestState')=='pass' or old['status'] in ('active','unverifiable'))):continue
                    domain=urllib.parse.urlsplit(url).hostname.removeprefix('www.')
                    if task['source'].startswith('New source discovery') and domain in known_domains:continue
                    if ebay is None and openai and ebay_id(url):
                        # Without eBay keys, eBay results go to the source-matched
                        # eBay verification below instead of a blocked page fetch.
                        ebay_results.append(dict(result,url=url));continue
                    if domain_finds[domain]>=6:continue
                    source=task.get('name') if task.get('domain')==domain else domain
                    markup='';offers=[]
                    if pages<240 and domain_pages[domain]<taste.page_cap(domain) and time.monotonic()-started<35*60:
                        pages+=1;domain_pages[domain]+=1
                        try:
                            if reverb_id(url):
                                # Reverb blocks item pages from the VM; its public API does not.
                                offer=reverb_offer(url,source,profile);live[domain]['pagesOpened']+=1
                                offers=[dict(offer,verifiedVia='api')] if offer else []
                            elif ebay is not None and ebay_id(url):
                                offer=ebay_offer_from(ebay.item(ebay_id(url)),url,source,profile);live[domain]['pagesOpened']+=1
                                offers=[dict(offer,verifiedVia='api')] if offer else []
                            else:
                                markup=fetch(url);live[domain]['pagesOpened']+=1
                                offers=[dict(o,verifiedVia='direct') for o in page_offers(markup,url,source,profile)]
                                if not offers and '/products/' in url:
                                    try:
                                        offer=shopify_offer(url,source,profile,fetch)
                                        if offer: offers=[dict(offer,verifiedVia='direct')]
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
                        # Never downgrade a verified offer or resurrect a lead whose page is gone.
                        if old and (old.get('verificationState')!='candidate' or old['status']=='removed'):c=None
                        if not c:continue
                    c['url']=canonical;report['listings'].append(c);submitted.add(canonical);domain_finds[domain]+=1;check['candidates']+=1
                    # The registry grows only from useful offer/lead evidence,
                    # not every arbitrary URL returned by a search engine.
                    live[domain]
                check['note']=f"Search query reviewed; {check['candidates']} offers/leads retained. Search indexing does not establish live availability."
            except Exception as error:
                check.update(status='failed',note='Search provider failed: '+type(error).__name__);failures.append(check['source']+' search failed')
            report['sources'].append(check);print(json.dumps({'source':check['source'],'status':check['status'],'candidates':check['candidates']}),flush=True)
    # Reverb and eBay through their official APIs: newest trumpets for the taste
    # model's makers and traits. They count as source coverage like any domain.
    by_item={}
    for listing in baseline:
        item=reverb_id(listing.get('url','')) or ebay_id(listing.get('url',''))
        if item:by_item.setdefault(item,listing)
    terms=marketplace_terms(taste,dt.datetime.now(dt.timezone.utc).date())
    def keep_market(c,item,domain,check,via='api'):
        old=by_item.get(item) or by_url.get(canonical_url(c['url']))
        if old and (old.get('acquired') or old.get('feedback',{}).get('interestState')=='pass' or old['status'] in ('active','unverifiable')):return
        if canonical_url(c['url']) in submitted or c['searchScore']<55 or domain_finds[domain]>=taste.page_cap(domain):return
        c.update(verificationState='verified',verifiedVia=via)
        if old:c.update(id=old['id'],url=old['url'])
        link_physical_horn(c,baseline);market_context(c,baseline)
        report['listings'].append(c);submitted.add(canonical_url(c['url']));domain_finds[domain]+=1;check['candidates']+=1;live[domain]['verifiedOffers']+=1
    def market_check(name,domain,specialty):
        return dict(source=name,domain=domain,query='; '.join(t or 'newest trumpets' for t in terms)[:10000],status='checked',candidates=0,note='',
                    pagesOpened=0,verifiedOffers=0,staleResults=0,geography='International',specialty=specialty)
    check=market_check('Reverb API','reverb.com','Marketplace (public listings API)')
    try:
        for term in terms:
            if pages>=240 or time.monotonic()-started>35*60:check['note']='Budget reached before every term was searched. ';break
            pages+=1
            for data in reverb_search(term):
                url=reverb_web_url(data)
                if not url:continue
                live['reverb.com']['pagesOpened']+=1
                native=str(data.get('listing_currency') or '').upper()
                if native and native!=str((data.get('price') or {}).get('currency') or '').upper():
                    if pages>=240:continue
                    pages+=1;data=reverb_item(str(data.get('id')))
                c=reverb_offer_from(data,url,'Reverb',profile)
                if c:keep_market(c,reverb_id(url),'reverb.com',check)
        check['note']+=f"{len(terms)} searches (newest first, trumpet category); {check['candidates']} verified offers retained."
    except Exception as error:
        check.update(status='failed',note='Reverb API failed: '+failure_reason(error));failures.append('Reverb API search failed')
    report['sources'].append(check)
    if ebay is None:
        # eBay without developer keys: discovery through the Responses web_search
        # tool, every candidate verified by a source-matched check (within the cap).
        check=market_check('eBay via web search','ebay.com','Marketplace (search-provider verified)')
        if not openai:
            check.update(status='skipped',query='',note='OPENAI_API_KEY not configured; eBay discovery needs the search provider.')
        else:
            try:
                def ebay_leads():
                    yield from ebay_results  # eBay results of the general search plan first
                    for term in terms:
                        if time.monotonic()-started>35*60 or verifier['calls']>=verify_limit:
                            check['note']='Budget reached before every term was searched. ';return
                        yield from search_openai('site:ebay.com '+(term or 'professional')+' Bb trumpet',openai)
                seen_items={ebay_id(u) for u in submitted}
                for result in ebay_leads():
                    item=ebay_id(result.get('url',''))
                    old=by_item.get(item)
                    if time.monotonic()-started>35*60:check['note']='Run time budget reached. ';break
                    if not item or item in seen_items or (old and old['status'] in ('active','unverifiable')) or verifier['calls']>=verify_limit:continue
                    seen_items.add(item)
                    url='https://ebay.com/itm/'+item
                    if canonical_url(url) in submitted or not candidate_hint(dict(result,url=url),'eBay',profile):continue
                    verifier['calls']+=1;live['ebay.com']['pagesOpened']+=1
                    try:data=verify_via_search(url,openai)
                    except Exception:data=None
                    if not data or data['state']!='available':
                        verifier['rejected']+=1;continue
                    verifier['accepted']+=1
                    # A search-provider answer is a lead, never a verified offer:
                    # no price, stock or date is asserted.
                    c=candidate_hint(dict(result,url=url,title=data['title'] or result.get('title','')),'eBay',profile)
                    if c and not old:
                        stated=f" Search provider reported it available{' at '+str(data['price'])+' '+str(data['currency']) if data['price'] is not None and data['currency'] else ''} (unverified)."
                        c.update(url=url,sourceListingId=item,evidence='Search-provider lead (cited eBay source); not a market observation',searchRationale=c['searchRationale']+stated)
                        report['listings'].append(c);submitted.add(canonical_url(url));domain_finds['ebay.com']+=1;check['candidates']+=1
                check['note']+=f"{len(terms)} web searches; {check['candidates']} eBay leads from cited sources (unverified)."
            except Exception as error:
                check.update(status='failed',note='eBay web search failed: '+failure_reason(error));failures.append('eBay web search failed')
    else:
        check=market_check('eBay Browse API','ebay.com','Marketplace (official Browse API)')
        try:
            for term in terms:
                if pages>=240 or time.monotonic()-started>35*60:check['note']='Budget reached before every term was searched. ';break
                pages+=1
                for summary in ebay.search(term):
                    item=str(summary.get('legacyItemId') or ebay_id(summary.get('itemWebUrl','')))
                    if not item or pages>=240:continue
                    url='https://ebay.com/itm/'+item
                    # Screen the summary first; only promising items cost a getItem call.
                    screened=ebay_offer_from(summary,url,'eBay',profile)
                    if not screened or screened['searchScore']<55 or (by_item.get(item) or {}).get('status')=='active':continue
                    pages+=1;live['ebay.com']['pagesOpened']+=1
                    c=ebay_offer_from(ebay.item(item),url,'eBay',profile)
                    if c:keep_market(c,item,'ebay.com',check)
            check['note']+=f"{len(terms)} searches (newly listed, trumpet category); {check['candidates']} verified offers retained."
        except Exception as error:
            check.update(status='failed',note='eBay Browse API failed: '+failure_reason(error));failures.append('eBay Browse API search failed')
    report['sources'].append(check)
    print(json.dumps({'source':'marketplace APIs','reverb':domain_finds['reverb.com'],'ebay':domain_finds['ebay.com']}),flush=True)
    if verifier['calls']:
        notes.append(f"Search-provider verification: {verifier['calls']} blocked listings tried, {verifier['accepted']} verified from a matching source, {verifier['rejected']} left as failed checks")
    # The 240-page bound is by design; reaching it is coverage information, not a failure.
    if taste.active:
        plan_keys={taste.source_key(d) for d in known_domains}|{k for q in MAKER_QUERIES for k in taste.makers_in(q)}
        notes.append(f"Taste steering: {sum(taste.favored(k) for k in plan_keys)} favored and {sum(taste.rejected(k) for k in plan_keys)} demoted makers/sources")
    if pages>=240:notes.append('Discovery page budget (240) reached; remaining promising results retained as unverified leads')
    # Attribute live evidence once per actual domain; query success is separate.
    by_domain={s['domain']:s for s in report['sources'] if s.get('domain')}
    for domain,counts in live.items():
        if domain not in by_domain:
            if not domain_finds[domain] and domain not in known_domains: continue
            check=dict(source='Live pages / '+domain,domain=domain,status='checked' if counts['pagesOpened'] else 'skipped',candidates=domain_finds[domain],note='Direct listing pages reviewed; stock evidence is stored per offer.' if counts['pagesOpened'] else 'Unverified search lead discovered; no live page accessed.',**counts)
            report['sources'].append(check)
        else:by_domain[domain].update(counts)
    report['error']='; '.join(dict.fromkeys(failures+notes))[:10000]
    if failures:report['status']='partial'
    result=submit_run(client,report);print(json.dumps(result),flush=True)
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
        result=submit_run(client,report);print(json.dumps(result));return 0
    return daily(client)

if __name__=='__main__':
    raise SystemExit(main())
