import copy
import json
import unittest
import urllib.error
from unittest.mock import patch
import runner

PRODUCT={'@type':'Product','name':'Taylor Chicago II Bb trumpet raw brass upswept', 'description':'One-off professional Bb trumpet', 'brand':{'name':'Taylor'},'model':'Chicago II','productID':'123','offers':{'@type':'Offer','price':'2400','priceCurrency':'USD','availability':'https://schema.org/InStock'},'image':'https://shop.test/horn.jpg'}
PROFILE={'priorityMakers':['Taylor'],'favoredAttributes':{'upswept':2},'dislikedAttributes':{}}
def page(p): return '<script type="application/ld+json">'+json.dumps(p)+'</script>'

class RunnerTests(unittest.TestCase):
    def test_product_and_graph(self):
        self.assertEqual(len(runner.products(page({'@graph':[PRODUCT]}))),1)
        c=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE)
        self.assertGreaterEqual(c['searchScore'],80);self.assertEqual(c['price'],2400);self.assertEqual(c['status'],'active')
    def test_inspiration_feed_is_a_bounded_bonus(self):
        base=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE)['searchScore']
        inspired=dict(PROFILE,inspirations=[{'maker':'Taylor','model':'','tags':['raw brass','upswept bell'],'priority':'someday','why':'love it'}]*20)
        boosted=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',inspired)['searchScore']
        self.assertGreater(boosted,base)
        self.assertLessEqual(boosted-base,6)
    def test_ambiguous_or_ordinary_not_invented(self):
        for offers in [{}, {'@type':'AggregateOffer','lowPrice':100}, {'price':100,'availability':'Maybe'}]:
            p=copy.deepcopy(PRODUCT);p['offers']=offers
            self.assertIsNone(runner.normalize_product(p,'https://shop.test/horn','Shop',PROFILE))
        p=copy.deepcopy(PRODUCT);p['name']='Yamaha YTR-2330 Bb trumpet';p['description']='Production student trumpet';p['brand']={'name':'Yamaha'}
        self.assertIsNone(runner.normalize_product(p,'https://shop.test/horn','Shop',PROFILE))
    def test_recheck_sold_removed_and_unknown(self):
        c=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE);c['id']='one'
        p=copy.deepcopy(PRODUCT);p['offers']['availability']='https://schema.org/SoldOut';p['offers']['price']='2200'
        result=runner.recheck(c,lambda _:page(p),PROFILE)
        self.assertEqual(result['status'],'sold');self.assertEqual(result['price'],2200);self.assertEqual(c['price'],2400)
        self.assertIsNone(runner.recheck(c,lambda _:'Login required',PROFILE))
        def missing(_): raise urllib.error.HTTPError(c['url'],404,'gone',None,None)
        self.assertEqual(runner.recheck(c,missing,PROFILE)['status'],'removed')
        def denied(_): raise urllib.error.HTTPError(c['url'],403,'denied',None,None)
        with self.assertRaises(urllib.error.HTTPError): runner.recheck(c,denied,PROFILE)
    def test_recommendation_not_wrong_price(self):
        c=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE)
        p=copy.deepcopy(PRODUCT);p['name']='Harrelson MUSE trumpet';p['productID']='other'
        self.assertIsNone(runner.recheck(c,lambda _:page(p),PROFILE))
        self.assertIsNone(runner.recheck(c,lambda _:page([PRODUCT,PRODUCT]),PROFILE))
        self.assertEqual(runner.page_offers(page([PRODUCT,PRODUCT]),c['url'],'Dealer',PROFILE),[])
    def test_candidate_hints_never_invent_market_facts(self):
        c=runner.candidate_hint({'url':'https://shop.test/products/harrelson-muse-raw-brass-trumpet'},'Dealer',{'priorityMakers':['Harrelson']})
        self.assertEqual(c['verificationState'],'candidate');self.assertEqual(c['status'],'stale')
        self.assertIsNone(c['price']);self.assertEqual(c['serialNumber'],'');self.assertEqual(c['images'],[])
        self.assertIsNotNone(runner.candidate_hint({'url':'https://gregblackmouthpieces.com/products/taylor-chicago-46-ii-standard-bb-trumpet','title':'Taylor Chicago 46 II Standard Bb Trumpet – Greg Black Mouthpieces'},'Dealer',PROFILE))
        for url in ['https://shop.test/collections/taylor-trumpet','https://shop.test/blog/taylor-trumpet','https://shop.test/products/taylor-mouthpiece','https://shop.test/products/yamaha-ytr2330-trumpet','https://shop.test/products/monette-prana-mouthpieces-used','https://shop.test/del-quadro-custom-trumpets/for-sale','https://shop.test/products/harrelson-trumpet-stand']:
            self.assertIsNone(runner.candidate_hint({'url':url},'Dealer',PROFILE))
    def test_dealer_metadata_requires_explicit_stock_currency_price(self):
        markup='<title>Taylor Chicago II trumpet</title><meta property="product:price:amount" content="2100"><meta property="product:price:currency" content="USD">'
        self.assertEqual(runner.page_offers(markup,'https://shop.test/product/taylor','Dealer',PROFILE),[])
        c=runner.page_offers(markup+'<meta property="product:availability" content="in stock">','https://shop.test/product/taylor','Dealer',PROFILE)[0]
        self.assertEqual(c['price'],2100);self.assertEqual(c['status'],'active')
        self.assertEqual(runner.page_offers(markup+'<meta property="price" content="12">','https://shop.test/product/taylor','Dealer',PROFILE),[])
    def test_shopify_scaled_prices_including_yen_and_ambiguous_variants(self):
        product={'title':'Taylor Chicago II trumpet','id':7,'variants':[{'price':100000,'available':True}]}
        def get(url): return json.dumps({'currency':'JPY'} if url.endswith('/cart.js') else product)
        c=runner.shopify_offer('https://shop.test/products/taylor','Dealer',PROFILE,get)
        self.assertEqual(c['price'],1000);self.assertEqual(c['currency'],'JPY')
        product['variants'].append({'price':250000,'available':True})
        self.assertIsNone(runner.shopify_offer('https://shop.test/products/taylor','Dealer',PROFILE,get))
        product['variants']=[{'price':100,'available':None}]
        self.assertIsNone(runner.shopify_offer('https://shop.test/products/taylor','Dealer',PROFILE,get))
    def test_candidate_promotes_only_unambiguous_offer(self):
        c=runner.candidate_hint({'url':'https://shop.test/products/taylor-chicago-trumpet'},'Dealer',PROFILE);c['id']='hint'
        result=runner.recheck(c,lambda _:page(PRODUCT),PROFILE)
        self.assertEqual(result['id'],'hint');self.assertEqual(result['verificationState'],'verified');self.assertEqual(result['price'],2400)
        self.assertIsNone(runner.recheck(c,lambda _:page([PRODUCT,PRODUCT]),PROFILE))
        self.assertEqual(runner.page_offers(page([PRODUCT,PRODUCT]),c['url'],'Dealer',PROFILE),[])
    def test_all_priority_maker_searches_are_present(self):
        queries=' '.join(runner.MAKER_QUERIES).lower()
        for name in ['taylor','harrelson','ar resonance','monette','adams','blackburn','van laar','inderbinen','lawler','lotus','eclipse','del quadro','bac','calicchio','schilke','benge']:
            self.assertIn(name,queries)
    def test_private_network_blocked(self):
        for url in ['http://shop.test/horn','file:///etc/passwd','https://user:pass@shop.test/horn']:
            with self.assertRaises(ValueError): runner.safe_remote(url)
        with patch('socket.getaddrinfo',return_value=[(None,None,None,None,('127.0.0.1',443))]):
            with self.assertRaises(ValueError): runner.safe_remote('https://shop.test/horn')
    def test_exceptional_production_models_are_ranked(self):
        for title in ['Schilke HC1 Handcraft Bb trumpet','Yamaha YTR-921X Bb trumpet serial 002','Olds Super Recording Bb trumpet','Schilke B5 gold plate Bb trumpet']:
            p=copy.deepcopy(PRODUCT);p['name']=title;p['model']=title;p['description']='Professional Bb trumpet';p['brand']={'name':title.split()[0]}
            c=runner.normalize_product(p,'https://shop.test/horn','Shop',PROFILE)
            self.assertIsNotNone(c);self.assertGreaterEqual(c['searchScore'],55)
    def test_source_diversity(self):
        self.assertGreaterEqual(len(runner.SOURCES),25);self.assertIn('TC Gakki / Japanese shops',runner.SOURCES);self.assertIn('European specialist dealers',runner.SOURCES)
    def test_openai_search_uses_only_completed_tool_sources(self):
        payload={'status':'completed','output':[
            {'type':'message','content':[{'text':'https://invented.test/horn'}]},
            {'type':'web_search_call','status':'completed','action':{'type':'search','sources':[
                {'url':'https://shop.test/horn'}, {'url':'https://shop.test/horn'},
                {'url':'https://other.test/horn'}, {'url':'http://shop.test/insecure'}]}}]}
        with patch.object(runner,'fetch',return_value=json.dumps(payload)) as fetch:
            results=runner.search_openai('site:shop.test used Bb trumpet','private-key')
            self.assertEqual(results,[{'url':'https://shop.test/horn'}])
            request=json.loads(fetch.call_args.args[2])
            self.assertIn('site:shop.test',request['input'])
            self.assertNotIn('private-key',request['input'])
    def test_grounded_citation_titles_and_negative_domain_filters(self):
        payload={'status':'completed','output':[{'type':'message','content':[{'annotations':[{'type':'url_citation','url':'https://ebay.com/itm/123','title':'Taylor Chicago II Bb trumpet'},{'type':'url_citation','url':'https://invented.test/horn','title':'Harrelson trumpet'}]}]},{'type':'web_search_call','status':'completed','action':{'type':'search','sources':[{'url':'https://ebay.com/itm/123'},{'url':'https://reverb.com/item/123'}]}}]}
        with patch.object(runner,'fetch',return_value=json.dumps(payload)):
            results=runner.search_openai('Taylor trumpet -site:reverb.com','key')
        self.assertEqual(results,[{'url':'https://ebay.com/itm/123','title':'Taylor Chicago II Bb trumpet'}])
        self.assertIsNotNone(runner.candidate_hint(results[0],'eBay',PROFILE))
    def test_openai_incomplete_or_unsearched_results_fail(self):
        for payload in [{'status':'incomplete','output':[]}, {'status':'completed','output':[{'type':'message','content':[]}]}]:
            with patch.object(runner,'fetch',return_value=json.dumps(payload)):
                with self.assertRaises(ValueError):runner.search_openai('Bb trumpet','private-key')
    def test_openai_recovers_temporary_provider_errors(self):
        payload={'status':'completed','output':[{'type':'web_search_call','status':'completed','action':{'type':'search','sources':[{'url':'https://shop.test/horn'}]}}]}
        errors=[urllib.error.HTTPError('https://api.openai.com/v1/responses',503,'unavailable',None,None),TimeoutError('timeout')]
        with patch.object(runner,'fetch',side_effect=errors+[json.dumps(payload)]) as fetch, patch.object(runner.time,'sleep'):
            self.assertEqual(runner.search_openai('Bb trumpet','private-key'),[{'url':'https://shop.test/horn'}])
            self.assertEqual(fetch.call_count,3)
    def test_openai_auth_failures_and_exhausted_retries_fail(self):
        for code,want in [(401,1),(500,3)]:
            error=urllib.error.HTTPError('https://api.openai.com/v1/responses',code,'failed',None,None)
            with patch.object(runner,'fetch',side_effect=error) as fetch, patch.object(runner.time,'sleep'):
                with self.assertRaises(urllib.error.HTTPError):runner.search_openai('Bb trumpet','private-key')
                self.assertEqual(fetch.call_count,want)

if __name__=='__main__': unittest.main()

class AdaptiveWatchTests(unittest.TestCase):
    def test_rotation_expansion_and_exploration_are_reserved(self):
        import datetime as dt
        universe=[dict(domain=f'dealer{i}.test',name=f'Dealer {i}',lastSearched=None,geography='International',specialty='specialist') for i in range(60)]
        profile={**PROFILE,'sourceUniverse':universe}
        first=runner.search_plan(profile,dt.date(2026,10,7))
        second=runner.search_plan(profile,dt.date(2026,10,8))
        selected={q['domain'] for q in first if q['domain']}
        self.assertEqual(len(selected),36)
        self.assertNotEqual(selected,{q['domain'] for q in second if q['domain']})
        self.assertEqual(sum(q['source'].startswith('New source') for q in first),4)
        self.assertGreaterEqual(sum(q['exploration'] for q in first)/len(first),.25)
        for q in first:
            if q['source'].startswith('New source'):
                for source in universe:self.assertIn('-site:'+source['domain'],q['query'])
        # Successful coverage advances the persistent rotation to older domains.
        for source in universe:
            if source['domain'] in selected:source['lastSearched']='2026-10-07T13:17:00Z'
        rotated=runner.search_plan(profile,dt.date(2026,10,8))
        unseen={s['domain'] for s in universe if not s['lastSearched']}
        self.assertTrue(unseen.issubset({q['domain'] for q in rotated}))
    def test_notes_change_rank_but_preserve_exploration(self):
        p=copy.deepcopy(PRODUCT)
        neutral=runner.normalize_product(p,'https://shop.test/item','Dealer',{'priorityMakers':['Taylor']})
        profile={'priorityMakers':['Taylor'],'notePreferences':{'favoredAttributes':{'upswept':2},'dislikedAttributes':{},'makerWeights':{},'pricePreferences':[]}}
        positive=runner.normalize_product(p,'https://shop.test/item','Dealer',profile)
        self.assertGreater(positive['searchScore'],neutral['searchScore'])
        profile['notePreferences']['pricePreferences']=[{'maker':'Taylor','currency':'USD','referencePrice':2200}]
        costly=runner.normalize_product(p,'https://shop.test/item','Dealer',profile)
        self.assertIsNotNone(costly);self.assertLess(costly['searchScore'],positive['searchScore'])
        p['brand']={'name':'Unfamiliar Builder'};p['name']='Unfamiliar Builder one-off prototype Bb trumpet unusual bell engineering';p['description']='Rare custom professional instrument'
        surprise=runner.normalize_product(p,'https://shop.test/item','Dealer',profile)
        self.assertGreaterEqual(surprise['searchScore'],65)
    def test_live_stock_badge_outranks_cached_structured_stock(self):
        c=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE)
        markup=page(PRODUCT)+'<h1>'+PRODUCT['name']+'</h1><div class="product-stock">Sold out</div>'
        checked=runner.recheck(c,lambda _:markup,PROFILE)
        self.assertEqual(checked['status'],'sold')
        related=page(PRODUCT)+'<h1>'+PRODUCT['name']+'</h1><section class="related-products"><div class="product-stock">Sold out</div></section>'
        self.assertEqual(runner.recheck(c,lambda _:related,PROFILE)['status'],'active')
        unrelated='<h1>Another trumpet</h1><div class="product-stock">Sold out</div>'
        self.assertIsNone(runner.recheck(c,lambda _:unrelated,PROFILE))
        pending='<h1>'+PRODUCT['name']+'</h1><div class="availability">Pending</div>'
        self.assertEqual(runner.recheck(c,lambda _:pending,PROFILE)['status'],'stale')
    def test_verified_updates_preserve_details_and_extract_shipping(self):
        c=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE)
        p=copy.deepcopy(PRODUCT);p['description']='Serial: AB-123. Artist provenance documentation added.'
        p['itemCondition']='https://schema.org/UsedCondition';p['additionalProperty']=[{'name':'Bore','value':'.460'},{'name':'Provenance','value':'Artist owned'}]
        p['offers']['shippingDetails']={'shippingRate':{'value':40,'currency':'USD'}}
        checked=runner.recheck(c,lambda _:page(p),PROFILE)
        self.assertIn('documentation',checked['description']);self.assertEqual(checked['shipping'],40)
        self.assertEqual(checked['details']['bore'],'.460');self.assertEqual(checked['serialNumber'],'AB-123')
    def test_serial_or_exact_image_required_for_crosspost_link(self):
        old={'maker':'Taylor','model':'Chicago II','serialNumber':'123','images':['https://shop.test/unique.jpg'],'hornId':'physical','verificationState':'verified'}
        same={'maker':'Taylor','model':'Chicago II','serialNumber':'123','images':[]}
        self.assertEqual(runner.link_physical_horn(same,[old])['hornId'],'physical')
        another={'maker':'Taylor','model':'Chicago II','serialNumber':'124','images':old['images']}
        self.assertNotIn('hornId',runner.link_physical_horn(another,[old]))
        missing={'maker':'Taylor','model':'Chicago II','serialNumber':'','images':[]}
        self.assertNotIn('hornId',runner.link_physical_horn(missing,[old]))
        self.assertEqual(runner.canonical_url('https://www.ebay.com/itm/123456789012?itmmeta=track&utm_source=x'),'https://ebay.com/itm/123456789012')
        self.assertEqual(runner.listing_id('https://ebay.com/itm/trumpet/123456789012'),'123456789012')

class MarketContextTests(unittest.TestCase):
    def test_comparisons_require_three_distinct_verified_same_currency_horns(self):
        c=runner.normalize_product(PRODUCT,'https://shop.test/item','Dealer',PROFILE)
        baseline=[dict(c,id=str(i),hornId=str(i),url='https://shop.test/'+str(i),model=c['model'],price=3200,lastChecked='2026-10-07',verificationState='verified') for i in range(3)]
        result=runner.market_context(copy.deepcopy(c),baseline)
        self.assertIn('good value',result['tags']);self.assertIn('not completed-sale',result['searchRationale'])
        self.assertNotIn('good value',runner.market_context(copy.deepcopy(c),baseline[:2])['tags'])
        for old in baseline:old['currency']='JPY'
        self.assertNotIn('good value',runner.market_context(copy.deepcopy(c),baseline)['tags'])

class MakerIdentityTests(unittest.TestCase):
    def test_modifier_does_not_replace_original_maker(self):
        p=copy.deepcopy(PRODUCT);p['name']='Taylor Chicago 46 II / Harrelson-modified Bb trumpet';p['description']='Harrelson modifications and artist provenance'
        c=runner.normalize_product(p,'https://shop.test/horn','Dealer',PROFILE)
        self.assertEqual(c['maker'],'Taylor')

class DailyPersistenceTests(unittest.TestCase):
    def test_manual_run_rechecks_active_and_reports_actual_domain_evidence(self):
        candidate=runner.normalize_product(PRODUCT,'https://shop.test/products/horn','Shop',PROFILE)
        candidate.update(id='known',hornId='physical',verificationState='verified',acquired=False,lastChecked='2026-10-07',feedback={'interestState':'watch'})
        class Client:
            report=None
            def call(self,path,body=None):
                if path=='/profile':return copy.deepcopy(PROFILE)
                if path=='/due':return {'listings':[]}
                if path=='/listings':return {'listings':[candidate]}
                if path=='/runs':self.report=body;return {'status':body['status'],'runId':'saved'}
        client=Client()
        with patch.dict('os.environ',{'BRAVE_SEARCH_API_KEY':'test-only'}),patch.object(runner,'fetch',return_value=page(PRODUCT)),patch.object(runner,'search',return_value=[]),patch('builtins.print'):
            self.assertEqual(runner.daily(client),0)
        self.assertEqual(client.report['listings'][0]['id'],'known')
        evidence=[s for s in client.report['sources'] if s.get('domain')=='shop.test']
        self.assertEqual(sum(s['pagesOpened'] for s in evidence),1)
        self.assertEqual(sum(s['verifiedOffers'] for s in evidence),1)
        self.assertTrue(any(s['source'].startswith('New source discovery') for s in client.report['sources']))

class InternationalRecheckTests(unittest.TestCase):
    def test_unpublished_price_retains_original_currency_and_shipping(self):
        c=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE);c['shipping']=40
        p=copy.deepcopy(PRODUCT);p['offers'].pop('price');p['offers']['priceCurrency']='EUR';p['offers']['shippingDetails']={'shippingRate':{'value':50,'currency':'EUR'}}
        result=runner.recheck(c,lambda _:page(p),PROFILE)
        self.assertEqual((result['price'],result['currency'],result['shipping']),(2400,'USD',40))
        p['offers']['price']='2100';p['offers'].pop('shippingDetails')
        result=runner.recheck(c,lambda _:page(p),PROFILE)
        self.assertEqual((result['price'],result['currency'],result['shipping']),(2100,'EUR',None))
    def test_explicit_instrument_brand_outranks_modifier_name(self):
        p=copy.deepcopy(PRODUCT);p['name']='Harrelson modified Taylor Chicago 46 II Bb trumpet';p['brand']={'name':'Taylor'}
        self.assertEqual(runner.normalize_product(p,'https://shop.test/horn','Shop',PROFILE)['maker'],'Taylor')

class SerialEvidenceTests(unittest.TestCase):
    def test_unknown_serial_text_cannot_create_a_shared_physical_identity(self):
        for placeholder in ['unknown','not provided','N/A','0']:
            p=copy.deepcopy(PRODUCT);p['description']='Professional Bb trumpet. Serial: '+placeholder
            self.assertEqual(runner.normalize_product(p,'https://shop.test/horn','Dealer',PROFILE)['serialNumber'],'')

class DailyFreshnessTests(unittest.TestCase):
    """A blocked page or the bounded discovery budget must not pin runs to partial."""
    def client(self,due,baseline):
        class Client:
            report=None
            def call(self,path,body=None):
                if path=='/profile':return copy.deepcopy(PROFILE)
                if path=='/due':return {'listings':copy.deepcopy(due)}
                if path=='/listings':return {'listings':copy.deepcopy(baseline)}
                if path=='/runs':self.report=body;return {'status':body['status'],'runId':'saved'}
        return Client()
    def lead(self,i,domain,attempt=None,first='2026-10-07T00:00:00Z'):
        c=runner.candidate_hint({'url':f'https://{domain}/products/taylor-chicago-trumpet-{i}'},'Dealer',PROFILE)
        return dict(c,id=f'lead-{domain}-{i}',firstSeen=first,lastCheckAttempt=attempt,feedback={'interestState':'watch'})
    def watch(self,client,fetch,search=lambda *_:[]):
        with patch.dict('os.environ',{'BRAVE_SEARCH_API_KEY':'test-only'}),patch.object(runner,'fetch',side_effect=fetch),patch.object(runner,'search',side_effect=search),patch('builtins.print'):
            return runner.daily(client)
    def test_blocked_active_offer_is_recorded_not_partial_and_blocked_leads_are_free(self):
        active=runner.normalize_product(PRODUCT,'https://ebay.com/itm/297326287591','eBay',PROFILE)
        active.update(id='jericho',verificationState='verified',acquired=False,lastChecked='2026-10-07T01:23:47Z',feedback={'interestState':'watch'})
        leads=[self.lead(i,'ebay.com') for i in range(5)]+[self.lead(i,'shop.test') for i in range(3)]
        def fetch(url,*_):
            if 'ebay.com' in url:raise urllib.error.HTTPError(url,403,'Forbidden',None,None)
            return page(PRODUCT)
        client=self.client([active]+leads,[active]+leads)
        self.assertEqual(self.watch(client,fetch),0)
        report=client.report
        self.assertEqual(report['status'],'succeeded')
        failed={f['id']:f['reason'] for f in report['checkFailures']}
        self.assertIn('HTTP 403',failed['jericho'])
        self.assertIn('Recheck failed, recorded for retry: ebay.com / HTTP 403',report['error'])
        # Every eBay lead is recorded (so it rotates back), none consumes a check.
        self.assertTrue(all('skipped' in failed[l['id']] for l in leads[:5]))
        promoted={l['id'] for l in report['listings'] if l.get('verificationState')=='verified'}
        self.assertEqual(promoted,{l['id'] for l in leads[5:]})
        self.assertIn('3 queued leads checked (3 verified',report['error'])
        self.assertNotIn('jericho',{l['id'] for l in report['listings']})
    def test_unsupported_active_page_is_recorded(self):
        active=runner.normalize_product(PRODUCT,'https://shop.test/horn','Shop',PROFILE)
        active.update(id='known',verificationState='verified',acquired=False,feedback={'interestState':'watch'})
        client=self.client([],[active])
        self.assertEqual(self.watch(client,lambda *_:'<h1>Login required</h1>'),0)
        self.assertEqual(client.report['checkFailures'][0]['id'],'known')
        self.assertIn('single verifiable offer',client.report['checkFailures'][0]['reason'])
    def test_page_budget_is_a_note_and_search_failure_still_partial(self):
        counter=iter(range(100000))
        def search(*_):return [{'url':f'https://d{next(counter)}.test/products/plain-page'} for _ in range(10)]
        client=self.client([],[])
        self.assertEqual(self.watch(client,lambda *_:'<html></html>',search),0)
        self.assertEqual(client.report['status'],'succeeded')
        self.assertIn('page budget (240) reached',client.report['error'])
        client=self.client([],[])
        def broken(*_):raise urllib.error.HTTPError('https://api.search.brave.com',500,'down',None,None)
        self.assertEqual(self.watch(client,lambda *_:'',broken),2)
        self.assertEqual(client.report['status'],'partial')
    def test_leads_rotate_least_recently_attempted_first(self):
        verified=dict(self.lead(0,'v.test'),verificationState='verified',id='v')
        never_old=self.lead(1,'a.test',first='2026-10-07T00:00:00Z')
        never_new=self.lead(2,'a.test',first='2026-10-10T00:00:00Z')
        tried_long_ago=self.lead(3,'a.test',attempt='2026-10-08T13:20:00Z',first='2026-10-11T00:00:00Z')
        tried_yesterday=self.lead(4,'a.test',attempt='2026-10-10T13:20:00Z',first='2026-10-11T00:00:00Z')
        order=runner.recheck_order([tried_yesterday,never_old,verified,tried_long_ago,never_new])
        self.assertEqual([l['id'] for l in order],['v',never_new['id'],never_old['id'],tried_long_ago['id'],tried_yesterday['id']])
        # 41 never-tried leads: one waits, and is first the next day once the others record attempts.
        due=[self.lead(i,'shop.test',first=f'2026-10-{7+i%4:02d}T{i%24:02d}:00:00Z') for i in range(41)]
        client=self.client(due,due)
        self.assertEqual(self.watch(client,lambda *_:'<html>no offer</html>'),0)
        attempted={f['id'] for f in client.report['checkFailures']}
        self.assertEqual(len(attempted),40)
        waiting=[l for l in due if l['id'] not in attempted]
        for l in due:
            if l['id'] in attempted:l['lastCheckAttempt']='2026-10-11T13:20:00Z'
        self.assertEqual(runner.recheck_order(due)[0]['id'],waiting[0]['id'])
    def test_dead_lead_is_retired_without_market_facts(self):
        lead=self.lead(0,'shop.test')
        def gone(_):raise urllib.error.HTTPError(lead['url'],404,'gone',None,None)
        result=runner.recheck(lead,gone,PROFILE)
        self.assertEqual((result['status'],result['verificationState'],result['price']),('removed','candidate',None))
    def test_failure_reasons_are_short_and_non_sensitive(self):
        self.assertEqual(runner.failure_reason(urllib.error.HTTPError('https://x.test',403,'no',None,None)),'HTTP 403 (source blocks automated checks)')
        self.assertEqual(runner.failure_reason(urllib.error.HTTPError('https://x.test',500,'no',None,None)),'HTTP 500')
        self.assertEqual(runner.failure_reason(TimeoutError()),'timed out')
        self.assertEqual(runner.failure_reason(urllib.error.URLError(ConnectionResetError())),'network error (ConnectionResetError)')

class VerificationCoverageTests(unittest.TestCase):
    def test_single_product_microdata_verifies_and_related_cards_do_not(self):
        markup=('<title>Taylor Chicago II trumpet</title><div itemscope itemtype="https://schema.org/Product">'
                '<h1 itemprop="name">Taylor Chicago II trumpet</h1><span itemprop="price" content="2400">$2,400</span>'
                '<meta itemprop="priceCurrency" content="USD"><link itemprop="availability" href="https://schema.org/InStock"></div>')
        c=runner.page_offers(markup,'https://shop.test/taylor','Dealer',PROFILE)[0]
        self.assertEqual((c['price'],c['currency'],c['status']),(2400,'USD','active'))
        self.assertIn('microdata',c['evidence'])
        related=markup+'<div itemscope itemtype="https://schema.org/Product"><span itemprop="price" content="900"></span></div>'
        self.assertEqual(runner.page_offers(related,'https://shop.test/taylor','Dealer',PROFILE),[])
        self.assertEqual(runner.page_offers(markup.replace('<link itemprop="availability" href="https://schema.org/InStock">',''),'https://shop.test/taylor','Dealer',PROFILE),[])
    def test_accessories_are_not_horns(self):
        for title in ['Protec PB301CT Taylor Trumpet Case','AR Resonance First Aid','AR Resonance Backbores','AR Resonance Trumpet Top','Monette trumpet gig bag']:
            p=copy.deepcopy(PRODUCT);p['name']=title
            self.assertIsNone(runner.normalize_product(p,'https://shop.test/x','Shop',PROFILE),title)
            self.assertIsNone(runner.candidate_hint({'url':'https://shop.test/products/x','title':title},'Shop',PROFILE),title)
        for title in ['Taylor Chicago II Bb trumpet with hard case','Taylor Chicago II trumpet w/ gig bag','Taylor Chicago II trumpet and trumpet case']:
            p=copy.deepcopy(PRODUCT);p['name']=title
            self.assertIsNotNone(runner.normalize_product(p,'https://shop.test/x','Shop',PROFILE),title)
