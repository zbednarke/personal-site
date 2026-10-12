"""Reverb/eBay marketplace APIs and the source-matched search-provider verifier.
Fictional recorded fixtures only; no real listings, sellers or credentials."""
import copy
import json
import os
import unittest
import urllib.error
import urllib.parse
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
        checked, reason = runner.apply_search_verification(listing, data)
        self.assertIsNone(reason)
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

    def test_open_page_of_the_given_url_or_an_uncited_answer_is_not_evidence(self):
        """Review fix: the model opening the URL it was given (even when that
        fetch was blocked) must not satisfy the source match, and the answer
        must cite the listing."""
        opened = fixture('openai_verify_match.json')
        opened['output'][0]['action'] = {'type': 'open_page', 'url': EBAY_URL}
        opened['output'][1]['content'][0]['text'] = json.dumps({'url': EBAY_URL, 'title': None, 'state': 'ended', 'price': None, 'currency': None, 'shipping': None, 'evidence': ''})
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(opened)))
        found = fixture('openai_verify_match.json'); found['output'][0]['action']['type'] = 'find_in_page'
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(found)))
        uncited = fixture('openai_verify_match.json'); uncited['output'][1]['content'][0]['annotations'] = []
        self.assertIsNone(runner.verify_via_search(EBAY_URL, 'k', FakeResponses(uncited)))

    def test_a_single_sold_or_ended_report_never_retires_a_verified_offer(self):
        listing = {'id': 'known', 'url': EBAY_URL, 'title': 't', 'maker': 'Monette', 'model': 'm', 'source': 'eBay', 'price': 5000, 'currency': 'USD', 'status': 'active', 'verificationState': 'verified'}
        base = {'url': EBAY_URL, 'title': None, 'price': None, 'currency': None, 'shipping': None, 'evidence': ''}
        self.assertEqual(runner.apply_search_verification(listing, dict(base, state='unknown')), (None, None))
        for state, retired in (('sold', 'sold'), ('ended', 'removed')):
            checked, reason = runner.apply_search_verification(listing, dict(base, state=state), today='2026-10-11')
            self.assertIsNone(checked)
            self.assertEqual(reason, f'search provider reports {state} (unconfirmed)')
            # The same report on the same UTC day still does not retire it ...
            same_day = dict(listing, lastCheckError='ebay.com / eBay: ' + reason, lastCheckAttempt='2026-10-11T13:20:00Z')
            self.assertIsNone(runner.apply_search_verification(same_day, dict(base, state=state), today='2026-10-11')[0])
            # ... a different state recorded yesterday does not either ...
            other = dict(same_day, lastCheckAttempt='2026-10-10T13:20:00Z', lastCheckError='ebay.com / eBay: search provider reports available')
            self.assertIsNone(runner.apply_search_verification(other, dict(base, state=state), today='2026-10-11')[0])
            # ... agreement on two separate days does.
            next_day = dict(same_day, lastCheckAttempt='2026-10-10T13:20:00Z')
            checked, _ = runner.apply_search_verification(next_day, dict(base, state=state, price=1), today='2026-10-11')
            self.assertEqual((checked['status'], checked['price']), (retired, 5000))
        kept, _ = runner.apply_search_verification(listing, dict(base, state='available'))
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

    @staticmethod
    def reply_for(url, state='available'):
        r = fixture('openai_verify_match.json')
        r['output'][0]['action']['sources'] = [{'url': url + '?utm_source=search'}]
        r['output'][1]['content'][0]['annotations'][0]['url'] = url
        data = json.loads(r['output'][1]['content'][0]['text']); data['state'] = state
        r['output'][1]['content'][0]['text'] = json.dumps(data)
        return r

    def test_blocked_or_challenged_pages_fall_back_within_the_cap(self):
        listings = [self.blocked_dealer(0), self.blocked_dealer(1), self.blocked_dealer(2)]
        def fetch(url, *_):
            if 'blocked0' in url: raise http_error(403)
            if 'blocked1' in url: return '<html><head><title>Just a moment...</title></head><body>cf-chl-widget</body></html>'
            raise http_error(429)
        fake = FakeResponses(self.reply_for(listings[0]['url']), self.reply_for(listings[1]['url']))
        client = Client(baseline=listings)
        self.assertEqual(self.watch(client, fetch, fake, env={'TRUMPETS_VERIFY_LIMIT': '2'}), 0)
        verified = {l['id']: l for l in client.report['listings']}
        self.assertEqual(set(verified), {'blocked-0', 'blocked-1'})
        self.assertTrue(all(l['verifiedVia'] == 'search-provider' for l in verified.values()))
        self.assertEqual([f['id'] for f in client.report['checkFailures']], ['blocked-2'])  # over the cap
        self.assertIn('budget reached', client.report['checkFailures'][0]['reason'])
        self.assertIn('Search-provider verification: 2 blocked listings tried, 2 verified', client.report['error'])

    def test_mismatch_and_single_day_ended_are_recorded_as_failed_checks(self):
        client = Client(baseline=[self.blocked_dealer(0)])
        self.watch(client, lambda url, *_: (_ for _ in ()).throw(http_error(403)), FakeResponses(fixture('openai_verify_mismatch.json')))
        self.assertEqual(client.report['listings'], [])
        self.assertIn('HTTP 403', client.report['checkFailures'][0]['reason'])
        dealer = self.blocked_dealer(0)
        client = Client(baseline=[dealer])
        self.watch(client, lambda url, *_: (_ for _ in ()).throw(http_error(403)), FakeResponses(self.reply_for(dealer['url'], 'ended')))
        self.assertEqual(client.report['listings'], [])  # never retired on one report
        self.assertIn('search provider reports ended (unconfirmed)', client.report['checkFailures'][0]['reason'])

    def test_ebay_without_keys_checks_via_search_provider_and_discovers_only_leads(self):
        tracked = dict(runner.ebay_offer_from(fixture('ebay_item.json'), 'https://www.ebay.com/itm/monette-raja/900000000003', 'eBay', MONETTE),
                       id='ebay-known', verificationState='verified', acquired=False, feedback={'interestState': 'watch'})
        lead = dict(runner.candidate_hint({'url': 'https://www.ebay.com/itm/900000000005', 'title': 'Monette Raja Bb trumpet'}, 'eBay', MONETTE),
                    id='ebay-lead', feedback={'interestState': 'watch'})
        new = self.reply_for('https://www.ebay.com/itm/900000000004')
        fake = FakeResponses(fixture('openai_verify_match.json'), new)
        searches = []
        def search_openai(query, key):
            searches.append(query)
            return [{'url': 'https://www.ebay.com/itm/monette-prana-trumpet/900000000004', 'title': 'Monette Prana Bb trumpet'}] if len(searches) == 1 else []
        client = Client(baseline=[tracked, lead], due=[tracked, lead])
        self.watch(client, lambda url, *_: self.fail('eBay page fetched: ' + url), fake, search_openai=search_openai)
        found = {l.get('sourceListingId') or l.get('id'): l for l in client.report['listings']}
        self.assertEqual(found['900000000003']['price'], 4950)
        # A new eBay item from the search provider is a lead, never verified-active.
        discovered = found['900000000004']
        self.assertEqual((discovered['verificationState'], discovered['status'], discovered['price']), ('candidate', 'stale', None))
        self.assertNotIn('verifiedVia', discovered)
        ebay_queries = [q for q in searches if q.startswith('site:ebay.com ') and q.endswith(' Bb trumpet')]
        self.assertGreaterEqual(len(ebay_queries), 6)  # eBay discovery runs every time
        coverage = next(s for s in client.report['sources'] if s['source'] == 'eBay via web search')
        self.assertEqual((coverage['status'], coverage['candidates']), ('checked', 1))
        # Review fix: eBay leads skip the lead queue instead of failing with a misleading reason.
        self.assertEqual(client.report['checkFailures'], [])
        self.assertIn('1 eBay leads await review', client.report['error'])
        self.assertNotIn('Lead rotation', client.report['error'])


class HostAllowlistTests(unittest.TestCase):
    """Review fix: lookalike hosts are not eBay or Reverb."""
    def test_lookalikes_rejected(self):
        for url in ['https://ebay.attacker.example/itm/900000000003', 'https://ebay.com.evil.io/itm/900000000003',
                    'https://notebay.com/itm/900000000003', 'https://www.ebay.example/itm/900000000003', 'ftp://ebay.com/itm/900000000003']:
            self.assertEqual(runner.ebay_id(url), '', url)
            self.assertEqual(runner.listing_id(url), '', url)
            self.assertFalse(runner.same_listing(url, EBAY_URL), url)
        for url in ['https://www.ebay.de/itm/900000000003', 'https://m.ebay.com.au/itm/slug/900000000003', 'https://ebay.ch/itm/900000000003']:
            self.assertEqual(runner.ebay_id(url), '900000000003', url)
        for url in ['https://reverb.com.evil.io/item/900000001', 'https://reverb.attacker.example/item/900000001', 'https://evilreverb.com/item/900000001']:
            self.assertEqual(runner.reverb_id(url), '', url)


class OldApiCompatibilityTests(unittest.TestCase):
    """Review fix: a runner deployed before its API still records runs."""
    def test_unknown_field_rejection_is_retried_once_without_new_fields(self):
        import io
        sent = []
        class OldApi:
            def call(self, path, body=None):
                sent.append(copy.deepcopy(body))
                if any('verifiedVia' in l for l in body['listings']):
                    raise urllib.error.HTTPError('https://api.fixture.invalid/runs', 400, 'Bad Request', {}, io.BytesIO(b'{"error":"invalid run JSON"}'))
                return {'status': body['status'], 'runId': 'saved'}
        report = {'externalId': 'x', 'status': 'succeeded', 'listings': [{'id': 'a', 'verifiedVia': 'api', 'price': 1}]}
        with patch('builtins.print'):
            self.assertEqual(runner.submit_run(OldApi(), report)['runId'], 'saved')
        self.assertEqual(len(sent), 2); self.assertEqual(sent[1]['listings'], [{'id': 'a', 'price': 1}])
        class Broken:
            def call(self, path, body=None):
                raise urllib.error.HTTPError('https://api.fixture.invalid/runs', 400, 'Bad Request', {}, io.BytesIO(b'{"error":"listing identity conflict"}'))
        with self.assertRaises(urllib.error.HTTPError):
            runner.submit_run(Broken(), report)


class RecordedReverbSearchTests(unittest.TestCase):
    """A sanitized recording of the real search response (structure only;
    every value is fictional): the runner's own request, parsing and keep rules."""
    def fake_reverb(self, requests):
        search, eur = fixture('reverb_search_trumpets.json'), fixture('reverb_listing_eur_native.json')
        shown = copy.deepcopy(next(l for l in search['listings'] if l['id'] == 900000102))
        def fetch(url, headers=None, data=None, timeout=25):
            requests.append((url, dict(headers or {})))
            if url.startswith(runner.REVERB_SEARCH + '?'): return json.dumps(search)
            if url == runner.REVERB_API + '900000102':
                return json.dumps(eur if (headers or {}).get('X-Display-Currency') == 'EUR' else shown)
            raise AssertionError('unexpected fetch ' + url)
        return fetch

    def run_daily(self, monotonic=None):
        client = Client(); requests = []
        patches = [patch.dict('os.environ', {'BRAVE_SEARCH_API_KEY': '', 'OPENAI_API_KEY': '', 'EBAY_CLIENT_ID': ''}),
                   patch.object(runner, 'fetch', side_effect=self.fake_reverb(requests)), patch('builtins.print')]
        if monotonic: patches.append(patch.object(runner.time, 'monotonic', side_effect=monotonic))
        for p in patches: p.start()
        try: runner.daily(client)
        finally:
            for p in reversed(patches): p.stop()
        return client, requests

    def test_search_request_and_parse(self):
        requests = []
        listings = runner.reverb_search('Monette', self.fake_reverb(requests))
        self.assertEqual([l['id'] for l in listings], [900000101, 900000102, 900000103, 900000104])
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(requests[0][0]).query)
        self.assertEqual(query, {'category_uuid': [runner.REVERB_TRUMPETS], 'price_min': ['800'], 'sort': ['published_at|desc'], 'per_page': ['24'], 'query': ['Monette']})
        c = runner.reverb_offer_from(listings[0], runner.reverb_web_url(listings[0]), 'Reverb', MONETTE)
        # US_CON is Reverb's domestic US shipping region.
        self.assertEqual((c['maker'], c['price'], c['currency'], c['shipping'], c['status']), ('Monette', 9800, 'USD', 45, 'active'))
        self.assertEqual(c['url'], 'https://reverb.com/item/900000101-fictional-fixture-900000101')

    def test_daily_keeps_verified_offers_and_explains_the_rest(self):
        client, requests = self.run_daily()
        kept = {l['sourceListingId']: l for l in client.report['listings'] if l.get('source') == 'Reverb'}
        self.assertEqual(set(kept), {'900000101', '900000102'})
        # The EUR listing shown in USD was refetched and stored in EUR.
        self.assertEqual((kept['900000102']['price'], kept['900000102']['currency']), (3900, 'EUR'))
        self.assertIn(('https://api.reverb.com/api/listings/900000102', dict(runner.REVERB_HEADERS, **{'X-Display-Currency': 'EUR'})), requests)
        reverb = next(s for s in client.report['sources'] if s['source'] == 'Reverb API')
        self.assertEqual((reverb['status'], reverb['candidates']), ('checked', 2))
        self.assertIn('not a verifiable Bb trumpet offer', reverb['note'])  # Bach without traits, flugelhorn
        self.assertIn('tracked', reverb['note'])  # later terms return the same listings

    def test_reverb_runs_even_after_the_search_budgets_are_spent(self):
        # Production 0: discovery ran after the web search had used the run's
        # time (and page) budget and stopped before its first request.
        clock = iter([0.0] + [10.0 ** 6] * 10000)
        client, requests = self.run_daily(monotonic=lambda: next(clock))
        self.assertTrue(any(u.startswith(runner.REVERB_SEARCH + '?') for u, _ in requests))
        self.assertEqual(len([l for l in client.report['listings'] if l.get('source') == 'Reverb']), 2)


class SubmitTimeoutTests(unittest.TestCase):
    """The production crash: the API stored the run (26.6 s) but the runner's
    read timed out first and exited with a traceback."""
    REPORT = {'externalId': 'adaptive-watch-fixture', 'status': 'succeeded', 'listings': []}

    class Api:
        def __init__(self, posts, lookups):
            self.posts = list(posts); self.lookups = list(lookups); self.calls = []
        def call(self, path, body=None):
            self.calls.append(path)
            reply = (self.posts if body is not None else self.lookups).pop(0)
            if isinstance(reply, Exception): raise reply
            return reply

    def submit(self, api):
        sleeps = []
        with patch('builtins.print'):
            result = runner.submit_run(api, self.REPORT, sleep=sleeps.append)
        return result, sleeps

    def test_timeout_then_found_does_not_resubmit(self):
        api = self.Api([TimeoutError('The read operation timed out')], [{'found': True, 'runId': 'r1', 'status': 'succeeded'}])
        result, sleeps = self.submit(api)
        self.assertEqual((result['runId'], result['status'], result['recovered']), ('r1', 'succeeded', True))
        self.assertEqual(api.calls, ['/runs', '/runs?externalId=adaptive-watch-fixture'])
        self.assertEqual(sleeps, [15])

    def test_timeout_wrapped_in_urlerror_then_found_later(self):
        api = self.Api([urllib.error.URLError(TimeoutError('timed out'))], [{'found': False}, {'found': True, 'runId': 'r1', 'status': 'partial'}])
        result, sleeps = self.submit(api)
        self.assertEqual(result['status'], 'partial'); self.assertEqual(sleeps, [15, 30])

    def test_not_stored_resubmits_with_the_same_external_id(self):
        api = self.Api([TimeoutError(), {'runId': 'r2', 'status': 'succeeded'}], [{'found': False}] * 3)
        result, sleeps = self.submit(api)
        self.assertEqual(result['runId'], 'r2'); self.assertEqual(api.calls.count('/runs'), 2); self.assertEqual(sleeps, [15, 30, 60])

    def test_api_without_lookup_relies_on_the_idempotent_replay(self):
        api = self.Api([TimeoutError(), {'runId': 'r1', 'status': 'succeeded', 'replayed': True}], [http_error(404)])
        result, _ = self.submit(api)
        self.assertTrue(result['replayed']); self.assertEqual(api.calls.count('/runs'), 2)

    def test_other_errors_are_not_retried(self):
        api = self.Api([http_error(500)], [])
        with self.assertRaises(urllib.error.HTTPError): self.submit(api)
        self.assertEqual(api.calls, ['/runs'])

    def test_main_exits_non_zero_with_one_line_and_no_traceback(self):
        import io
        from contextlib import redirect_stderr
        err = io.StringIO()
        with patch.object(runner, 'run', side_effect=TimeoutError('The read operation timed out')), redirect_stderr(err):
            self.assertEqual(runner.main(), 1)
        self.assertEqual(err.getvalue().count('\n'), 1)
        self.assertNotIn('Traceback', err.getvalue())
        self.assertEqual(json.loads(err.getvalue()), {'error': 'trumpet runner failed: timed out waiting for the API'})
        err = io.StringIO()
        with patch.dict('os.environ', {}, clear=True), patch('sys.argv', ['runner.py']), redirect_stderr(err):
            self.assertEqual(runner.main(), 1)
        self.assertIn('missing environment variable TRUMPETS_API_URL', err.getvalue())

    def test_real_client_gives_the_submit_a_long_timeout(self):
        seen = []
        with patch.dict('os.environ', {'TRUMPETS_API_URL': 'https://api.fixture.invalid', 'TRUMPETS_MACHINE_TOKEN': 'fixture'}), \
                patch.object(runner, 'fetch', side_effect=lambda url, headers, data, timeout: seen.append((url, timeout)) or '{}'):
            client = runner.Client(); client.call('/runs', {}); client.call('/profile')
        self.assertEqual(seen, [('https://api.fixture.invalid/v1/trumpets/machine/runs', 180), ('https://api.fixture.invalid/v1/trumpets/machine/profile', 60)])


class EbayDiscoveryBudgetTests(unittest.TestCase):
    def test_rechecks_spending_the_verify_budget_do_not_starve_ebay_discovery(self):
        # Production 0: tracked eBay offers (no eBay keys) are rechecked through
        # the same search-provider budget, which left none for new leads.
        client = Client()
        searches = []
        def search_openai(query, key):
            searches.append(query)
            return [{'url': 'https://www.ebay.com/itm/monette-raja-trumpet/900000000077', 'title': 'Monette Raja Bb trumpet FICTIONAL'}]
        available = {'url': 'https://ebay.com/itm/900000000077', 'title': 'Monette Raja Bb trumpet FICTIONAL', 'state': 'available', 'price': None, 'currency': None, 'shipping': None, 'evidence': 'fixture'}
        with patch.dict('os.environ', {'BRAVE_SEARCH_API_KEY': '', 'OPENAI_API_KEY': 'fixture-key', 'EBAY_CLIENT_ID': '', 'TRUMPETS_VERIFY_LIMIT': '0'}), \
                patch.object(runner, 'search_plan', return_value=[]), patch.object(runner, 'reverb_search', return_value=[]), \
                patch.object(runner, 'search_openai', side_effect=search_openai), patch.object(runner, 'verify_via_search', return_value=available), patch('builtins.print'):
            runner.daily(client)
        self.assertTrue(any(q.startswith('site:ebay.com') for q in searches))
        ebay = next(s for s in client.report['sources'] if s['source'] == 'eBay via web search')
        self.assertEqual((ebay['status'], ebay['candidates']), ('checked', 1))
        self.assertIn('1 items checked', ebay['note'])


if __name__ == '__main__':
    unittest.main()
