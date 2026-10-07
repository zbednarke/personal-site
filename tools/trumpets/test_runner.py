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
