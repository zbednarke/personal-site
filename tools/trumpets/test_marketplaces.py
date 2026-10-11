"""Reverb/eBay marketplace APIs and the source-matched search-provider verifier.
Fictional recorded fixtures only; no real listings, sellers or credentials."""
import copy
import json
import os
import unittest
import urllib.error
from unittest.mock import patch
import runner
from test_runner import PRODUCT, PROFILE, page

FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')
MONETTE = dict(PROFILE, priorityMakers=['Monette', 'Taylor'])
EBAY_URL = 'https://ebay.com/itm/900000000003'


def fixture(name):
    with open(os.path.join(FIXTURES, name)) as f:
        return json.load(f)


class FakeResponses:
    """A fake Responses API endpoint: records requests, replays fixtures."""
    def __init__(self, *replies):
        self.replies = list(replies); self.requests = []
    def __call__(self, url, headers=None, data=None, *_):
        assert url == runner.OPENAI_RESPONSES, url
        self.requests.append((dict(headers or {}), json.loads(data)))
        reply = self.replies[min(len(self.requests), len(self.replies)) - 1]
        if isinstance(reply, Exception): raise reply
        return json.dumps(reply)


def http_error(code, retry_after=None):
    headers = {'Retry-After': retry_after} if retry_after else {}
    return urllib.error.HTTPError('https://api.fixture.invalid', code, 'error', headers, None)


class IdentityTests(unittest.TestCase):
    def test_reverb_ids_from_any_reverb_link(self):
        for url in ['https://reverb.com/item/900000001-fictional-fixture', 'https://www.reverb.com/item/900000001',
                    'https://applink.reverb.com/item/900000001-fixture?utm_source=app', 'https://m.reverb.com/item/900000001',
                    'https://applink.reverb.com/open?link=' + 'https%3A%2F%2Freverb.com%2Fitem%2F900000001-fixture']:
            self.assertEqual(runner.reverb_id(url), '900000001', url)
        for url in ['https://notreverb.com/item/900000001', 'https://reverb.com/p/fixture', 'https://evil.invalid/?u=https://reverb.com/item/1']:
            self.assertEqual(runner.reverb_id(url), '', url)

    def test_ebay_ids_and_same_listing(self):
        self.assertEqual(runner.ebay_id('https://www.ebay.com/itm/monette-fixture/900000000003?hash=item0'), '900000000003')
        self.assertEqual(runner.ebay_id('https://ebay.co.uk/itm/900000000003'), '900000000003')
        self.assertTrue(runner.same_listing('https://www.ebay.com/itm/slug/900000000003?hash=x', EBAY_URL))
        self.assertFalse(runner.same_listing('https://www.ebay.com/itm/900000000999', EBAY_URL))
        self.assertTrue(runner.same_listing('https://www.shop.test/horn/?utm_source=x', 'https://shop.test/horn'))
        self.assertFalse(runner.same_listing('https://shop.test/other', 'https://shop.test/horn'))


class BackoffTests(unittest.TestCase):
    def test_rate_limits_back_off_then_succeed_or_raise(self):
        sleeps = []
        calls = iter([http_error(429, '7'), http_error(503), '{"ok": true}'])
        def get(*_):
            value = next(calls)
            if isinstance(value, Exception): raise value
            return value
        self.assertEqual(runner.api_json('https://api.fixture.invalid/x', get=get, sleep=sleeps.append), {'ok': True})
        self.assertEqual(sleeps, [7, 4])
        with self.assertRaises(urllib.error.HTTPError):
            runner.api_json('https://api.fixture.invalid/x', get=lambda *_: (_ for _ in ()).throw(http_error(429, '999')), sleep=sleeps.append)
        self.assertEqual(sleeps[-2:], [30, 30])  # Retry-After is capped
        with self.assertRaises(urllib.error.HTTPError):  # not retried
            runner.api_json('https://api.fixture.invalid/x', get=lambda *_: (_ for _ in ()).throw(http_error(403)), sleep=self.fail)


class ReverbDiscoveryTests(unittest.TestCase):
    def test_search_uses_trumpet_category_newest_first(self):
        seen = []
        def get(url, headers=None, *_):
            seen.append((url, headers)); return json.dumps({'listings': [fixture('reverb_listing_live.json')]})
        listings = runner.reverb_search('Taylor', get)
        url, headers = seen[0]
        self.assertTrue(url.startswith('https://api.reverb.com/api/listings?'))
        for part in ['category_uuid=' + runner.REVERB_TRUMPETS, 'sort=published_at%7Cdesc', 'query=Taylor', 'price_min=800']:
            self.assertIn(part, url)
        self.assertEqual((headers['Accept'], headers['Accept-Version']), ('application/hal+json', '3.0'))
        self.assertEqual(len(listings), 1)

    def test_daily_discovers_reverb_offers_and_counts_coverage(self):
        client = Client()
        live = fixture('reverb_listing_live.json')
        with patch.dict('os.environ', {'BRAVE_SEARCH_API_KEY': '', 'OPENAI_API_KEY': '', 'EBAY_CLIENT_ID': ''}), \
                patch.object(runner, 'reverb_search', return_value=[live]), patch.object(runner, 'fetch', side_effect=AssertionError('no page fetch')), patch('builtins.print'):
            runner.daily(client)
        offers = [l for l in client.report['listings'] if l.get('sourceListingId') == '900000001']
        self.assertEqual(len(offers), 1)
        self.assertEqual((offers[0]['verifiedVia'], offers[0]['verificationState'], offers[0]['price']), ('api', 'verified', 3100))
        reverb = next(s for s in client.report['sources'] if s['source'] == 'Reverb API')
        self.assertEqual((reverb['status'], reverb['domain'], reverb['candidates'], reverb['verifiedOffers']), ('checked', 'reverb.com', 1, 1))
        self.assertGreaterEqual(reverb['pagesOpened'], 1)
        ebay = next(s for s in client.report['sources'] if s['domain'] == 'ebay.com')
        self.assertEqual(ebay['status'], 'skipped')


class Client:
    def __init__(self, baseline=(), due=(), profile=None):
        self.baseline = list(baseline); self.due = list(due); self.profile = profile or MONETTE; self.report = None
    def call(self, path, body=None):
        if path == '/profile': return copy.deepcopy(self.profile)
        if path == '/due': return {'listings': copy.deepcopy(self.due)}
        if path == '/listings': return {'listings': copy.deepcopy(self.baseline)}
        if path == '/runs': self.report = body; return {'status': body['status'], 'runId': 'saved'}


class EbayBrowseTests(unittest.TestCase):
    """The Browse API stays optional: used only when keys exist."""
    def test_token_cached_until_expiry_and_refreshed_on_401(self):
        clock = [0.0]; calls = []; item = fixture('ebay_item.json')
        replies = {'token': iter([{'access_token': 'fixture-token-1', 'expires_in': 7200}, {'access_token': 'fixture-token-2', 'expires_in': 7200}, {'access_token': 'fixture-token-3', 'expires_in': 7200}])}
        unauthorized = [True]
        def get(url, headers=None, data=None, *_):
            calls.append((url, dict(headers or {})))
            if url == runner.EBAY_TOKEN_URL:
                self.assertIn(b'grant_type=client_credentials', data); return json.dumps(next(replies['token']))
            if headers['Authorization'] == 'Bearer fixture-token-2' and unauthorized[0]:
                unauthorized[0] = False; raise http_error(401)
            return json.dumps(item)
        api = runner.EbayAPI('fixture-id', 'fixture-secret', get=get, clock=lambda: clock[0])
        api.item('900000000003'); api.item('900000000003')
        self.assertEqual(sum(u == runner.EBAY_TOKEN_URL for u, _ in calls), 1)
        clock[0] = 7200
        api.item('900000000003')  # expired -> new token -> 401 -> one more token
        self.assertEqual(sum(u == runner.EBAY_TOKEN_URL for u, _ in calls), 3)
        self.assertNotIn('fixture-secret', repr(api)); self.assertNotIn('fixture-token', repr(api))
        self.assertIn('legacy_item_id=900000000003', calls[1][0])
        self.assertEqual(calls[1][1]['X-EBAY-C-MARKETPLACE-ID'], 'EBAY_US')

    def test_item_facts_and_recheck(self):
        item = fixture('ebay_item.json')
        api = runner.EbayAPI('fixture-id', 'fixture-secret', get=lambda u, h=None, d=None: json.dumps({'access_token': 't', 'expires_in': 7200} if u == runner.EBAY_TOKEN_URL else item))
        c = runner.ebay_offer_from(item, EBAY_URL, 'eBay', MONETTE)
        self.assertEqual((c['status'], c['price'], c['currency'], c['shipping'], c['seller'], c['location']), ('active', 5200, 'USD', 60, 'fixture_seller', 'Fixtureville, Oregon, US'))
        self.assertEqual(c['postedAt'], '2026-10-10T15:00:00+00:00')
        tracked = dict(c, id='known', verificationState='verified', price=5400)
        result = runner.recheck(tracked, lambda *_: self.fail('page fetched'), MONETTE, ebay=api)
        self.assertEqual((result['price'], result['verifiedVia']), (5200, 'api'))
        sold = copy.deepcopy(item); sold['estimatedAvailabilities'] = [{'estimatedAvailabilityStatus': 'OUT_OF_STOCK'}]
        self.assertEqual(runner.ebay_facts(sold)['status'], 'sold')
        gone = runner.EbayAPI('i', 's', get=lambda u, h=None, d=None: json.dumps({'access_token': 't'}) if u == runner.EBAY_TOKEN_URL else (_ for _ in ()).throw(http_error(404)))
        self.assertEqual(runner.recheck(tracked, None, MONETTE, ebay=gone)['status'], 'removed')
        search = []
        api2 = runner.EbayAPI('i', 's', get=lambda u, h=None, d=None: (search.append(u) or json.dumps({'access_token': 't'} if u == runner.EBAY_TOKEN_URL else {'itemSummaries': []})))
        api2.search('Monette')
        self.assertIn('category_ids=16214', search[-1]); self.assertIn('sort=newlyListed', search[-1]); self.assertIn('buyingOptions', search[-1])


class SearchProviderVerifierTests(unittest.TestCase):
    def test_source_match_is_accepted_with_schema_and_model(self):
        fake = FakeResponses(fixture('openai_verify_match.json'))
        with patch.dict('os.environ', {'TRUMPETS_VERIFY_MODEL': 'fixture-verify-model'}):
            data = runner.verify_via_search(EBAY_URL, 'test-only', fake)
        self.assertEqual((data['state'], data['price'], data['currency']), ('available', 4950, 'USD'))
        headers, request = fake.requests[0]
        self.assertEqual(request['model'], 'fixture-verify-model')
        self.assertEqual(request['tools'], [{'type': 'web_search', 'search_context_size': 'medium'}])
        self.assertEqual(request['include'], ['web_search_call.action.sources'])
        self.assertEqual(request['text']['format']['type'], 'json_schema'); self.assertTrue(request['text']['format']['strict'])
        self.assertIn(EBAY_URL, request['input'])
        listing = dict(runner.ebay_offer_from(fixture('ebay_item.json'), EBAY_URL, 'eBay', MONETTE), id='known', verificationState='verified')
        checked = runner.apply_search_verification(listing, data)
        self.assertEqual((checked['status'], checked['price'], checked['shipping'], checked['verifiedVia']), ('active', 4950, 55, 'search-provider'))

    def test_mismatched_source_prose_or_bad_json_is_rejected(self):
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(fixture('openai_verify_mismatch.json'))))
        prose = fixture('openai_verify_match.json'); prose['output'][1]['content'][0]['text'] = 'It looks available for about $4,950.'
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(prose)))
        extra = fixture('openai_verify_match.json')
        extra['output'][1]['content'][0]['text'] = json.dumps({'url': EBAY_URL, 'title': None, 'state': 'available', 'price': 'cheap', 'currency': 'USD', 'shipping': None, 'evidence': ''})
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(extra)))
        incomplete = fixture('openai_verify_match.json'); incomplete['status'] = 'incomplete'
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(incomplete)))

    def test_conservative_states(self):
        listing = {'id': 'known', 'url': EBAY_URL, 'title': 't', 'maker': 'Monette', 'model': 'm', 'source': 'eBay', 'price': 5000, 'currency': 'USD', 'status': 'active'}
        base = {'url': EBAY_URL, 'title': None, 'price': None, 'currency': None, 'shipping': None, 'evidence': ''}
        self.assertIsNone(runner.apply_search_verification(listing, dict(base, state='unknown')))
        self.assertEqual(runner.apply_search_verification(listing, dict(base, state='sold'))['status'], 'sold')
        ended = runner.apply_search_verification(listing, dict(base, state='ended', price=1))
        self.assertEqual((ended['status'], ended['price']), ('removed', 5000))
        kept = runner.apply_search_verification(listing, dict(base, state='available'))
        self.assertEqual((kept['status'], kept['price']), ('active', 5000))

    def watch(self, client, fetch, fake, env=None, search_openai=lambda *_: []):
        environ = {'BRAVE_SEARCH_API_KEY': '', 'OPENAI_API_KEY': 'test-only', 'EBAY_CLIENT_ID': '', 'TRUMPETS_VERIFY_LIMIT': '60', **(env or {})}
        real = runner.verify_via_search
        with patch.dict('os.environ', environ), patch.object(runner, 'fetch', side_effect=fetch), patch.object(runner, 'reverb_search', return_value=[]), \
                patch.object(runner, 'search_openai', side_effect=search_openai), \
                patch.object(runner, 'verify_via_search', side_effect=lambda url, key: real(url, key, fake)), patch('builtins.print'):
            return runner.daily(client)

    def blocked_dealer(self, i):
        c = runner.normalize_product(PRODUCT, f'https://blocked{i}.test/horn', 'Dealer', PROFILE)
        return dict(c, id=f'blocked-{i}', verificationState='verified', acquired=False, feedback={'interestState': 'watch'})

    def test_blocked_or_challenged_pages_fall_back_within_the_cap(self):
        match = fixture('openai_verify_match.json')
        def reply(url):  # the fixture's source pointed at a dealer URL
            r = copy.deepcopy(match); r['output'][0]['action']['sources'] = [{'url': url + '?utm_source=search'}]; return r
        listings = [self.blocked_dealer(0), self.blocked_dealer(1), self.blocked_dealer(2)]
        def fetch(url, *_):
            if 'blocked0' in url: raise http_error(403)
            if 'blocked1' in url: return '<html><head><title>Just a moment...</title></head><body>cf-chl-widget</body></html>'
            raise http_error(429)
        fake = FakeResponses(reply(listings[0]['url']), reply(listings[1]['url']))
        client = Client(baseline=listings)
        self.assertEqual(self.watch(client, fetch, fake, env={'TRUMPETS_VERIFY_LIMIT': '2'}), 0)
        verified = {l['id']: l for l in client.report['listings']}
        self.assertEqual(set(verified), {'blocked-0', 'blocked-1'})
        self.assertTrue(all(l['verifiedVia'] == 'search-provider' for l in verified.values()))
        self.assertEqual([f['id'] for f in client.report['checkFailures']], ['blocked-2'])  # over the cap
        self.assertIn('Search-provider verification: 2 blocked listings tried, 2 verified', client.report['error'])

    def test_mismatch_is_recorded_as_a_failed_check(self):
        client = Client(baseline=[self.blocked_dealer(0)])
        self.watch(client, lambda url, *_: (_ for _ in ()).throw(http_error(403)), FakeResponses(fixture('openai_verify_mismatch.json')))
        self.assertEqual(client.report['listings'], [])
        self.assertIn('HTTP 403', client.report['checkFailures'][0]['reason'])

    def test_ebay_without_keys_uses_the_search_provider_for_checks_and_discovery(self):
        tracked = dict(runner.ebay_offer_from(fixture('ebay_item.json'), 'https://www.ebay.com/itm/monette-raja/900000000003', 'eBay', MONETTE),
                       id='ebay-known', verificationState='verified', acquired=False, feedback={'interestState': 'watch'})
        new = copy.deepcopy(fixture('openai_verify_match.json'))
        new['output'][0]['action']['sources'] = [{'url': 'https://www.ebay.com/itm/900000000004'}]
        new['output'][1]['content'][0]['text'] = json.dumps({'url': 'https://www.ebay.com/itm/900000000004', 'title': 'Monette Prana Bb trumpet raw brass FICTIONAL', 'state': 'available', 'price': 6100, 'currency': 'USD', 'shipping': None, 'evidence': 'Buy It Now'})
        fake = FakeResponses(fixture('openai_verify_match.json'), new)
        searches = []
        def search_openai(query, key):
            searches.append(query)
            return [{'url': 'https://www.ebay.com/itm/monette-prana-trumpet/900000000004', 'title': 'Monette Prana Bb trumpet'}] if len(searches) == 1 else []
        client = Client(baseline=[tracked])
        self.watch(client, lambda url, *_: self.fail('eBay page fetched: ' + url), fake, search_openai=search_openai)
        found = {l.get('sourceListingId') or l.get('id'): l for l in client.report['listings']}
        self.assertEqual(found['900000000003']['price'], 4950)
        self.assertEqual((found['900000000004']['price'], found['900000000004']['verifiedVia']), (6100, 'search-provider'))
        ebay_queries = [q for q in searches if q.startswith('site:ebay.com ') and q.endswith(' Bb trumpet')]
        self.assertGreaterEqual(len(ebay_queries), 6)  # eBay discovery runs every time
        coverage = next(s for s in client.report['sources'] if s['source'] == 'eBay via web search')
        self.assertEqual((coverage['status'], coverage['candidates']), ('checked', 1))
        self.assertEqual(client.report['checkFailures'], [])


if __name__ == '__main__':
    unittest.main()
