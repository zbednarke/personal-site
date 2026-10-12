"""Registered sources the daily search leaves out on purpose (NOT_SEARCHED):
Schagerl's own shop sells new instruments only, so its site search could only
end "failed" with 0 candidates and turn the run partial. Fictional dealers use
*.test hosts."""
import copy
import datetime as dt
import unittest
from unittest.mock import patch
import runner

SCHAGERL = dict(domain='schagerl.com', name='Schagerl', geography='Austria', specialty='maker / demo', lastSearched=None)
DAY = dt.date(2026, 10, 12)


def universe(n=40):
    dealers = [dict(domain=f'dealer{i}.test', name=f'Dealer {i}', geography='International', specialty='specialist dealer',
                    lastSearched='2026-10-0%dT13:17:00Z' % (1 + i % 9)) for i in range(n)]
    return [SCHAGERL] + dealers


class Client:
    def __init__(self, profile):
        self.profile = profile; self.report = None
    def call(self, path, body=None):
        if path == '/profile': return copy.deepcopy(self.profile)
        if path in ('/due', '/listings'): return {'listings': []}
        if path == '/runs': self.report = body; return {'status': body['status'], 'runId': 'saved'}
        return {}


class NotSearchedTests(unittest.TestCase):
    def test_plan_never_site_searches_schagerl_even_when_it_is_the_oldest_source(self):
        plan = runner.search_plan({'sourceUniverse': universe()}, DAY)
        sites = [t['domain'] for t in plan if t['domain']]
        self.assertNotIn('schagerl.com', sites)
        self.assertFalse(any('site:schagerl.com' in t['query'].replace('-site:schagerl.com', '') for t in plan))
        # Its slot goes to another registered source.
        self.assertEqual(len(sites), 36)
        # Source discovery still excludes it: it is a known domain, not a new one.
        discovery = [t for t in plan if t['source'].startswith('New source discovery')]
        self.assertTrue(discovery)
        for task in discovery: self.assertIn('-site:schagerl.com', task['query'])

    def test_www_and_case_variants_are_left_out_too(self):
        variant = dict(SCHAGERL, domain='WWW.Schagerl.com')
        plan = runner.search_plan({'sourceUniverse': [variant] + universe()[1:]}, DAY)
        self.assertNotIn('www.schagerl.com', [t['domain'].lower() for t in plan])
        self.assertEqual(runner.not_searched({'sourceUniverse': [variant]}),
                         [('Schagerl', 'schagerl.com', runner.NOT_SEARCHED['schagerl.com'])])

    def test_other_maker_sources_are_still_searched(self):
        maker = dict(domain='maker.test', name='Fixture Maker', geography='Austria', specialty='maker / demo', lastSearched=None)
        plan = runner.search_plan({'sourceUniverse': [SCHAGERL, maker]}, DAY)
        self.assertEqual([t['domain'] for t in plan if t['domain']], ['maker.test'])

    def daily(self, search_openai, sources):
        client = Client({'priorityMakers': ['Taylor'], 'sourceUniverse': sources})
        with patch.dict('os.environ', {'BRAVE_SEARCH_API_KEY': '', 'OPENAI_API_KEY': 'fixture-key', 'EBAY_CLIENT_ID': '', 'TRUMPETS_VERIFY_LIMIT': '0'}), \
                patch.object(runner, 'reverb_search', return_value=[]), patch.object(runner, 'search_openai', side_effect=search_openai), \
                patch('builtins.print'):
            runner.daily(client)
        return client.report

    def test_run_explains_schagerl_and_is_not_partial(self):
        queries = []
        def search_openai(query, key):
            queries.append(query)
            # What the site search did in production: no usable search, 0 candidates.
            if 'site:schagerl.com' in query.replace('-site:schagerl.com', ''): raise ValueError('No completed web search')
            return []
        report = self.daily(search_openai, universe(5))
        self.assertFalse(any('site:schagerl.com' in q.replace('-site:schagerl.com', '') for q in queries))
        self.assertTrue(any(q.startswith('site:dealer0.test') for q in queries))
        self.assertEqual(report['status'], 'succeeded')
        self.assertFalse([s for s in report['sources'] if s['status'] == 'failed'])
        self.assertNotIn('schagerl.com', [s.get('domain') for s in report['sources']])
        self.assertIn('Not searched by design: Schagerl (schagerl.com): maker shop selling new instruments only', report['error'])

    def test_a_real_search_failure_elsewhere_still_makes_the_run_partial(self):
        def search_openai(query, key):
            if query.startswith('site:dealer0.test'): raise ValueError('No completed web search')
            return []
        report = self.daily(search_openai, universe(5))
        failed = [s for s in report['sources'] if s['status'] == 'failed']
        self.assertEqual([(s['domain'], s['candidates'], s['note']) for s in failed],
                         [('dealer0.test', 0, 'Search provider failed: ValueError')])
        self.assertEqual(report['status'], 'partial')


if __name__ == '__main__':
    unittest.main()
