"""Taste steering, Reverb API verification and accessory filtering.
Fictional fixtures only: no real listing, profile or feedback data."""
import copy
import datetime as dt
import json
import os
import unittest
import urllib.error
from unittest.mock import patch
import runner
from test_runner import PRODUCT, PROFILE

FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')


def fixture(name):
    with open(os.path.join(FIXTURES, name)) as f:
        return json.load(f)


class AccessoryTests(unittest.TestCase):
    HORNS = ['Taylor Chicago II Bb trumpet, Harrelson mod kit, 5mm mouthpiece',
             'Taylor Chicago II trumpet with extra leadpipe',
             'Taylor Chicago II trumpet + 2 mouthpieces and practice mute',
             'Taylor Chicago II Bb trumpet incl. lead pipe set and stand',
             'Taylor Chicago II trumpet w/ valve caps, backbore and case']
    ACCESSORIES = ['Taylor trumpet mouthpiece 5mm', 'Taylor Chicago leadpipe for trumpet', 'Harrelson trumpet stand',
                   'AR Resonance Trumpet Top', 'Monette trumpet gig bag', 'Piccolo trumpet in A, Taylor',
                   'Taylor mouthpiece fits Bb trumpet', 'Taylor Bb trumpet valve caps']

    def test_included_accessories_do_not_reject_the_horn(self):
        for title in self.HORNS:
            p = copy.deepcopy(PRODUCT); p['name'] = title
            self.assertIsNotNone(runner.normalize_product(p, 'https://shop.test/x', 'Shop', PROFILE), title)
            self.assertIsNotNone(runner.candidate_hint({'url': 'https://shop.test/products/x', 'title': title}, 'Shop', PROFILE), title)
            self.assertFalse(runner.is_accessory(title.lower()), title)

    def test_the_item_itself_being_an_accessory_is_rejected(self):
        for title in self.ACCESSORIES:
            p = copy.deepcopy(PRODUCT); p['name'] = title
            self.assertIsNone(runner.normalize_product(p, 'https://shop.test/x', 'Shop', PROFILE), title)
            self.assertIsNone(runner.candidate_hint({'url': 'https://shop.test/products/x', 'title': title}, 'Shop', PROFILE), title)


class ReverbApiTests(unittest.TestCase):
    URL = 'https://reverb.com/item/900000001-fictional-fixture'

    def api(self, *bodies):
        calls = []
        def get(url, headers=None, *_):
            calls.append((url, dict(headers or {})))
            if url.startswith('https://reverb.com/'):
                raise AssertionError('Reverb HTML pages must not be fetched')
            body = bodies[min(len(calls), len(bodies)) - 1]
            if isinstance(body, Exception): raise body
            return json.dumps(body)
        return get, calls

    def test_live_listing_verified_through_the_api(self):
        get, calls = self.api(fixture('reverb_listing_live.json'))
        c = runner.reverb_offer(self.URL, 'Reverb', PROFILE, get)
        self.assertEqual(calls[0][0], 'https://api.reverb.com/api/listings/900000001')
        self.assertEqual(calls[0][1]['Accept-Version'], '3.0')
        self.assertEqual((c['status'], c['price'], c['currency'], c['shipping']), ('active', 3100, 'USD', 45))
        self.assertEqual(c['postedAt'], '2026-10-10T12:06:18+00:00')
        self.assertEqual((c['sourceListingId'], c['seller'], c['location']), ('900000001', 'Fixture Brass Shop', 'Fixture City, US'))
        self.assertEqual(c['images'], ['https://fixtures.invalid/reverb/photo-1.jpg'])
        self.assertIn('Reverb public listings API', c['evidence'])

    def test_display_currency_is_refetched_in_the_listing_currency(self):
        sold = fixture('reverb_listing_sold_eur.json')
        converted = copy.deepcopy(sold); converted['price'] = {'amount': '4700.00', 'currency': 'USD'}
        get, calls = self.api(converted, sold)
        c = runner.reverb_offer('https://reverb.com/item/900000002-fixture', 'Reverb', PROFILE, get)
        self.assertEqual(calls[1][1].get('X-Display-Currency'), 'EUR')
        self.assertEqual((c['status'], c['price'], c['currency'], c['shipping']), ('sold', 4100, 'EUR', 80))
        # A converted display price is never stored.
        self.assertIsNone(runner.reverb_facts(converted))

    def test_recheck_uses_the_api_for_state_price_shipping_and_post_date(self):
        tracked = runner.reverb_offer(self.URL, 'Reverb', PROFILE, self.api(fixture('reverb_listing_live.json'))[0])
        tracked.update(id='known', verificationState='verified', price=3300, shipping=None)
        never = lambda *_: (_ for _ in ()).throw(AssertionError('HTML fetched'))
        changed = fixture('reverb_listing_live.json'); changed['price'] = {'amount': '2950.00', 'currency': 'USD'}
        result = runner.recheck(tracked, never, PROFILE, self.api(changed)[0])
        self.assertEqual((result['id'], result['status'], result['price'], result['shipping']), ('known', 'active', 2950, 45))
        self.assertEqual(result['postedAt'], '2026-10-10T12:06:18+00:00')
        ended = fixture('reverb_listing_live.json'); ended['state'] = {'slug': 'ended'}
        self.assertEqual(runner.recheck(tracked, never, PROFILE, self.api(ended)[0])['status'], 'removed')
        sold = fixture('reverb_listing_live.json'); sold['state'] = {'slug': 'sold'}
        self.assertEqual(runner.recheck(tracked, never, PROFILE, self.api(sold)[0])['status'], 'sold')
        gone = urllib.error.HTTPError('https://api.reverb.com/api/listings/900000001', 404, 'gone', None, None)
        removed = runner.recheck(tracked, never, PROFILE, self.api(gone)[0])
        self.assertEqual((removed['status'], removed['price']), ('removed', 3300))
        draft = fixture('reverb_listing_live.json'); draft['state'] = {'slug': 'draft'}
        self.assertIsNone(runner.recheck(tracked, never, PROFILE, self.api(draft)[0]))
        blocked = urllib.error.HTTPError('https://api.reverb.com/api/listings/900000001', 429, 'slow down', None, None)
        with self.assertRaises(urllib.error.HTTPError), patch('time.sleep') as slept:
            runner.recheck(tracked, never, PROFILE, self.api(blocked)[0])
        self.assertEqual(slept.call_count, 2)  # rate limit: bounded backoff, then a recorded failure

    def test_lead_on_reverb_is_promoted_by_the_api(self):
        lead = dict(url=self.URL, title='Reverb lead', maker='Taylor', model='Lead', source='Reverb', id='lead', verificationState='candidate', status='stale')
        result = runner.recheck(lead, None, PROFILE, self.api(fixture('reverb_listing_live.json'))[0])
        self.assertEqual((result['id'], result['verificationState'], result['price']), ('lead', 'verified', 3100))

    def test_daily_search_verifies_reverb_results_through_the_api(self):
        class Client:
            report = None
            def call(self, path, body=None):
                if path == '/profile': return copy.deepcopy(PROFILE)
                if path == '/due': return {'listings': []}
                if path == '/listings': return {'listings': []}
                if path == '/runs': self.report = body; return {'status': body['status'], 'runId': 'saved'}
        def fetch(url, headers=None, *_):
            if url.startswith('https://api.reverb.com/'): return json.dumps(fixture('reverb_listing_live.json'))
            if 'reverb.com' in url: raise urllib.error.HTTPError(url, 403, 'Forbidden', None, None)
            return '<html></html>'
        client = Client()
        with patch.dict('os.environ', {'BRAVE_SEARCH_API_KEY': 'test-only'}), patch.object(runner, 'fetch', side_effect=fetch), \
                patch.object(runner, 'search', side_effect=lambda q, k: [{'url': self.URL}] if 'site:reverb.com' in q else []), patch('builtins.print'):
            runner.daily(client)
        verified = [l for l in client.report['listings'] if l.get('verificationState') == 'verified']
        self.assertEqual(len(verified), 1)
        self.assertEqual((verified[0]['price'], verified[0]['shipping']), (3100, 45))


def weight(feature, w, sd, n, kind=None, label=None):
    return {'feature': feature, 'kind': kind or feature.split(':')[0], 'label': label or feature.split(':', 1)[1], 'weight': w, 'sd': sd, 'n': n}


class TasteSteeringTests(unittest.TestCase):
    DAY = dt.date(2026, 10, 7)

    def universe(self):
        return [dict(domain=f'dealer{i}.test', name=f'Dealer {i}', lastSearched=None, geography='International', specialty='specialist') for i in range(60)]

    def test_no_taste_keeps_the_existing_plan(self):
        profile = {**PROFILE, 'sourceUniverse': self.universe()}
        self.assertEqual(runner.search_plan(profile, self.DAY), runner.search_plan({**profile, 'taste': {'weights': []}, 'preferences': {}}, self.DAY))

    def test_budget_moves_toward_value_and_uncertainty_and_away_from_rejection(self):
        universe = self.universe()
        universe[59]['lastSearched'] = '2026-10-06T13:17:00Z'  # would normally wait its turn
        taste = {'observations': 40, 'priorSd': 1, 'weights': [
            weight('maker:harrelson', 1.2, 0.3, 8, label='Harrelson'),
            weight('maker:taylor', -1.1, 0.3, 9, label='Taylor'),
            weight('finish:raw brass', 0.9, 0.3, 10, label='Raw brass'),
            weight('source:dealer59.test', 1.0, 0.3, 6),
            weight('source:dealer0.test', -1.5, 0.3, 6)]}
        plan = runner.search_plan({**PROFILE, 'sourceUniverse': universe, 'taste': taste}, self.DAY)
        sites = [t for t in plan if t.get('domain')]
        self.assertEqual(len({t['domain'] for t in sites}), 36)
        # The liked source is searched first (it gets the page budget first) ...
        self.assertEqual(sites[0]['domain'], 'dealer59.test')
        # ... and the consistently rejected one only if nothing else needs a slot.
        self.assertNotIn('dealer0.test', {t['domain'] for t in sites})
        makers = [t['query'] for t in plan if t['source'].startswith('Maker search')]
        self.assertEqual(len(makers), 6)
        self.assertIn('Harrelson', makers[0])
        self.assertFalse(any(q.startswith('Taylor Chicago II') for q in makers))
        self.assertTrue(any(q.startswith('raw brass') for q in makers))
        # Exploration stays reserved.
        self.assertGreaterEqual(sum(t['exploration'] for t in plan) / len(plan), .25)
        self.assertEqual(sum(t['source'].startswith('New source') for t in plan), 4)
        steer = runner.Taste({'taste': taste})
        self.assertEqual((steer.page_cap('dealer59.test'), steer.page_cap('dealer0.test'), steer.page_cap('other.test')), (16, 6, 12))

    def test_uncertain_makers_are_explored_before_confidently_mediocre_ones(self):
        taste = {'observations': 30, 'priorSd': 1, 'weights': [
            weight('maker:monette', 0.0, 0.95, 1, label='Monette'),
            weight('maker:harrelson', 0.1, 0.1, 30, label='Harrelson')]}
        steer = runner.Taste({'taste': taste})
        self.assertGreater(steer.query_value('AR Resonance Feroce Monette used Bb trumpet sale'), steer.query_value('Harrelson MUSE Bravura Summit used Bb trumpet sale'))

    def test_owner_stated_preferences_steer_without_reactions(self):
        profile = {**PROFILE, 'sourceUniverse': self.universe(), 'preferences': {'likedMakers': ['Lawler'], 'dislikedMakers': ['Taylor'], 'likedTraitKeys': ['bell:upswept'], 'dislikedTraitKeys': []}}
        makers = [t['query'] for t in runner.search_plan(profile, self.DAY) if t['source'].startswith('Maker search')]
        self.assertIn('Lawler', makers[0])
        self.assertFalse(any(q.startswith('Taylor Chicago II') for q in makers))
        self.assertTrue(any(q.startswith('upswept bell') for q in makers))


if __name__ == '__main__':
    unittest.main()
