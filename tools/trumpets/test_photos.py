"""Listing photos: Reverb API, OpenGraph/Twitter meta, schema.org images,
Shopify product JSON, WooCommerce/Squarespace galleries, and the one-time
backfill. Fictional fixtures only (fixtures.invalid / *.test hosts); no real
listings, sellers or photos."""
import copy
import io
import json
import unittest
import urllib.error
from unittest.mock import patch
import runner
from test_runner import PRODUCT, PROFILE, page

REVERB_ITEM = {
    'id': 900000201, 'title': 'Taylor Chicago II Bb trumpet raw brass · FICTIONAL FIXTURE', 'make': 'Taylor',
    'state': {'slug': 'live'}, 'listing_currency': 'USD', 'price': {'amount': '3100.00', 'currency': 'USD'},
    'photos': [
        {'_links': {'full': {'href': 'https://rvb-img.fixtures.invalid/i/full-1.jpg'}, 'large_crop': {'href': 'https://rvb-img.fixtures.invalid/i/crop-1.jpg'}, 'thumbnail': {'href': 'https://rvb-img.fixtures.invalid/i/thumb-1.jpg'}}},
        {'_links': {'large_crop': {'href': 'https://rvb-img.fixtures.invalid/i/crop-2.jpg'}}},
        {'_links': {}},
        'not a photo',
    ],
    '_links': {'photo': {'href': 'https://rvb-img.fixtures.invalid/i/full-1.jpg'}, 'web': {'href': 'https://reverb.com/item/900000201-fixture'}},
}
OG_PAGE = '''<html><head><title>Harrelson Summit · FIXTURE</title>
<meta property="og:image" content="http://dealer.test/uploads/summit-1_w640.jpeg">
<meta property="og:image" content="/uploads/summit-2_w640.jpeg">
<meta property="og:image" content="https://dealer.test/uploads/site-logo.png">
<meta property="og:image" content="data:image/gif;base64,R0lGODlhAQABAAAAACw=">
<meta name="twitter:image" content="https://dealer.test/uploads/summit-3.jpeg">
<meta property="og:image" content="https://www.facebook.com/tr?id=1&ev=PageView">
<meta property="og:image" content="https://dealer.test/img/placeholder-image.jpg">
<meta property="og:image" content="https://dealer.test/img/pixel.gif?w=1">
</head><body>
<div class="product-gallery"><img src="/uploads/summit-1_w2560.jpeg"><img data-src="/uploads/summit-4_w2560.jpeg" src="data:image/svg+xml,%3Csvg%3E"></div>
<img src="/uploads/related-horn.jpeg">
</body></html>'''


class ImageCleaningTests(unittest.TestCase):
    def test_absolute_https_large_and_no_logos_placeholders_data_or_pixels(self):
        images = runner.page_images(OG_PAGE, 'https://dealer.test/store/p1/summit.html')
        # Gallery first (largest variants), then meta; _w640 duplicates of a
        # gallery photo collapse; relative and http:// URLs become https.
        self.assertEqual(images, ['https://dealer.test/uploads/summit-1_w2560.jpeg', 'https://dealer.test/uploads/summit-4_w2560.jpeg',
                                  'https://dealer.test/uploads/summit-2_w640.jpeg', 'https://dealer.test/uploads/summit-3.jpeg'])

    def test_at_most_six(self):
        markup = ''.join(f'<meta property="og:image" content="https://dealer.test/p/horn-{i}.jpg">' for i in range(10))
        self.assertEqual(len(runner.page_images(markup, 'https://dealer.test/p')), runner.MAX_IMAGES)

    def test_schema_org_product_image_objects_lists_and_protocol_relative(self):
        product = dict(PRODUCT, image=[{'@type': 'ImageObject', 'url': '//cdn.shopify.fixtures.invalid/files/horn_1024x.jpg?v=2&width=800'},
                                       {'@type': 'ImageObject', 'contentUrl': 'https://shop.test/horn-back.jpg'}, 'https://shop.test/no-image.png'])
        self.assertEqual(runner.page_images(page(product), 'https://shop.test/products/horn'),
                         ['https://cdn.shopify.fixtures.invalid/files/horn_1600x.jpg?v=2', 'https://shop.test/horn-back.jpg'])
        # normalize_product (verified offers) uses the same cleaning.
        c = runner.normalize_product(product, 'https://shop.test/products/horn', 'Shop', PROFILE)
        self.assertEqual(c['images'][0], 'https://cdn.shopify.fixtures.invalid/files/horn_1600x.jpg?v=2')

    def test_category_pages_give_no_photos_but_a_product_described_twice_does(self):
        other = dict(PRODUCT, name='Harrelson MUSE trumpet', image='https://shop.test/muse.jpg')
        self.assertEqual(runner.page_images(page([PRODUCT, other]) + OG_PAGE, 'https://shop.test/c'), [])
        twice = page(PRODUCT) + page(dict(PRODUCT, image=['https://shop.test/horn-2.jpg']))
        self.assertEqual(runner.page_images(twice, 'https://shop.test/p'), ['https://shop.test/horn.jpg', 'https://shop.test/horn-2.jpg'])

    def test_woocommerce_and_squarespace_galleries(self):
        woo = '''<div class="woocommerce-product-gallery"><div class="woocommerce-product-gallery__image" data-thumb="https://wp.test/wp-content/uploads/horn-100x100.jpg">
<a href="https://wp.test/wp-content/uploads/horn.jpg"><img src="https://wp.test/wp-content/uploads/horn-600x400.jpg" data-large_image="https://wp.test/wp-content/uploads/horn.jpg"></a></div>
<div class="woocommerce-product-gallery__image"><a href="https://wp.test/wp-content/uploads/bell.jpg"><img src="https://wp.test/wp-content/uploads/bell-600x400.jpg"></a></div></div>'''
        self.assertEqual(runner.page_images(woo, 'https://wp.test/product/horn/'), ['https://wp.test/wp-content/uploads/horn.jpg', 'https://wp.test/wp-content/uploads/bell.jpg'])
        sqs = '''<div class="ProductItem-gallery"><div class="ProductItem-gallery-slides">
<img data-image="https://images.squarespace-cdn.com/content/horn.jpg" data-src="https://images.squarespace-cdn.com/content/horn.jpg?format=300w" src="">
<img data-image="https://images.squarespace-cdn.com/content/valves.jpg"></div></div>
<meta itemprop="image" content="https://images.squarespace-cdn.com/content/horn.jpg?format=1500w">'''
        self.assertEqual(runner.page_images(sqs, 'https://sqs.test/shop/p/horn'), ['https://images.squarespace-cdn.com/content/horn.jpg?format=1500w', 'https://images.squarespace-cdn.com/content/valves.jpg?format=1500w'])

    def test_gallery_survives_nested_elements_and_ends_with_its_element(self):
        markup = '''<div class="product-gallery"><div><span><img src="https://g.test/one.jpg"></span></div><br><p><img src="https://g.test/two.jpg"></p></div>
<div class="related"><img src="https://g.test/related.jpg"></div>'''
        self.assertEqual(runner.page_images(markup, 'https://g.test/p'), ['https://g.test/one.jpg', 'https://g.test/two.jpg'])

    def test_only_the_main_gallery_never_related_or_loop_products(self):
        # Review fix: Woodmart/Porto product loops use product-image classes;
        # their photos are other horns and must never fill this listing's slots.
        markup = '''<div class="woocommerce-product-gallery images">
  <div class="woocommerce-product-gallery__image"><a href="https://wp.test/wp-content/uploads/main-1.jpg"><img src="https://wp.test/wp-content/uploads/main-1-600x400.jpg"></a></div>
  <div class="woocommerce-product-gallery__image"><a href="https://wp.test/wp-content/uploads/main-2.jpg"><img src="https://wp.test/wp-content/uploads/main-2-600x400.jpg"></a></div>
</div>
<div class="product-image"><img src="https://wp.test/wp-content/uploads/porto-loop.jpg"></div>
<section class="related products"><h2>Related products</h2>
  <ul class="products columns-4">
    <li class="product"><a class="product-image-link" href="/product/other"><img src="https://wp.test/wp-content/uploads/other-horn.jpg"></a>
      <div class="product-gallery"><img src="https://wp.test/wp-content/uploads/other-gallery.jpg"></div>
      <meta itemprop="image" content="https://wp.test/wp-content/uploads/other-micro.jpg"></li>
  </ul>
</section>
<div class="upsells products"><div class="product-gallery"><img src="https://wp.test/wp-content/uploads/upsell.jpg"></div></div>
<div class="cross-sells"><img itemprop="image" src="https://wp.test/wp-content/uploads/cross.jpg"></div>
<div class="product-gallery"><img src="https://wp.test/wp-content/uploads/second-gallery.jpg"></div>'''
        self.assertEqual(runner.page_images(markup, 'https://wp.test/product/horn/'), ['https://wp.test/wp-content/uploads/main-1.jpg', 'https://wp.test/wp-content/uploads/main-2.jpg'])
        loop = '<ul class="products"><li><div class="product-gallery"><img src="https://wp.test/a.jpg"></div></li></ul><div class="product-images"><img src="https://wp.test/b.jpg"></div>'
        self.assertEqual(runner.page_images(loop, 'https://wp.test/shop/'), [])

    def test_shopify_product_json(self):
        product = {'title': 'Taylor Chicago II trumpet', 'images': ['//shop.test/cdn/shop/files/horn_grande.jpg?v=1', '//shop.test/cdn/shop/files/bell_1600x.jpg?v=1'],
                   'media': [{'media_type': 'image', 'src': '//shop.test/cdn/shop/files/horn_1600x.jpg?v=1'}, {'media_type': 'video', 'src': '//shop.test/v.mp4'}]}
        seen = []
        def get(url): seen.append(url); return json.dumps(product)
        self.assertEqual(runner.shopify_images('https://shop.test/products/taylor-chicago?variant=3', get), ['https://shop.test/cdn/shop/files/horn_1600x.jpg?v=1', 'https://shop.test/cdn/shop/files/bell_1600x.jpg?v=1'])
        self.assertEqual(seen, ['https://shop.test/products/taylor-chicago.js'])
        self.assertEqual(runner.shopify_images('https://shop.test/collections/trumpets', get), [])
        # A verified Shopify offer keeps its photos (they are protocol-relative in the JSON).
        offer = dict(product, id=7, vendor='Taylor', variants=[{'price': 250000, 'available': True}])
        def get_offer(url): return json.dumps({'currency': 'USD'} if url.endswith('/cart.js') else offer)
        c = runner.shopify_offer('https://shop.test/products/taylor-chicago', 'Shop', PROFILE, get_offer)
        self.assertEqual(c['images'], ['https://shop.test/cdn/shop/files/horn_1600x.jpg?v=1', 'https://shop.test/cdn/shop/files/bell_1600x.jpg?v=1'])

    def test_reverb_api_photos_prefer_full_and_large_crop(self):
        self.assertEqual(runner.reverb_images(REVERB_ITEM), ['https://rvb-img.fixtures.invalid/i/full-1.jpg', 'https://rvb-img.fixtures.invalid/i/crop-2.jpg'])
        only_main = {'_links': {'photo': {'href': 'https://rvb-img.fixtures.invalid/i/main.jpg'}}}
        self.assertEqual(runner.reverb_images(only_main), ['https://rvb-img.fixtures.invalid/i/main.jpg'])
        self.assertEqual(runner.reverb_facts(REVERB_ITEM)['images'][0], 'https://rvb-img.fixtures.invalid/i/full-1.jpg')

    def test_ebay_image_urls_prefer_the_large_size(self):
        self.assertEqual(runner.clean_images(['https://i.ebayimg.fixtures.invalid/images/g/abc/s-l225.jpg']), ['https://i.ebayimg.fixtures.invalid/images/g/abc/s-l225.jpg'])
        self.assertEqual(runner.enlarge('https://i.ebayimg.com/images/g/abc/s-l225.jpg'), 'https://i.ebayimg.com/images/g/abc/s-l1600.jpg')

    def test_sizes_are_bounded_never_full_size_originals(self):
        # Review fix: request ~1600 px variants, not multi-megabyte originals.
        self.assertEqual(runner.enlarge('https://shop.test/cdn/shop/files/horn.jpg?v=1&width=4000'), 'https://shop.test/cdn/shop/files/horn_1600x.jpg?v=1')
        self.assertEqual(runner.enlarge('https://cdn.shopify.com/s/files/1/x/horn_600x600_crop_center.jpg?v=2'), 'https://cdn.shopify.com/s/files/1/x/horn_1600x.jpg?v=2')
        self.assertEqual(runner.enlarge('https://cdn.shopify.com/s/files/1/x/horn_master.png'), 'https://cdn.shopify.com/s/files/1/x/horn_1600x.png')
        self.assertEqual(runner.enlarge('https://images.squarespace-cdn.com/content/v1/horn.jpg'), 'https://images.squarespace-cdn.com/content/v1/horn.jpg?format=1500w')
        self.assertEqual(runner.enlarge('https://images.squarespace-cdn.com/content/v1/horn.jpg?format=2500w'), 'https://images.squarespace-cdn.com/content/v1/horn.jpg?format=1500w')
        # Other hosts keep the URL the page gave (no guessing at originals).
        for url in ['https://wp.test/wp-content/uploads/horn-600x400.jpg', 'https://dealer.test/uploads/summit_w640.jpeg', 'https://rvb-img.fixtures.invalid/i/s--x--/quality=medium-low,height=800,width=800/horn.jpg']:
            self.assertEqual(runner.enlarge(url), url)


class RecheckPhotoTests(unittest.TestCase):
    def tracked(self, **kw):
        c = runner.normalize_product(PRODUCT, 'https://shop.test/horn', 'Shop', PROFILE)
        c.update({'id': '11111111-1111-4111-8111-111111111111', 'images': [], **kw})
        return c

    def test_reverb_recheck_sets_photos_on_a_listing_without_them(self):
        listing = self.tracked(url='https://reverb.com/item/900000201-fixture', source='Reverb')
        found = {}
        checked = runner.recheck(listing, None, PROFILE, get_api=lambda u, h=None: json.dumps(REVERB_ITEM), found=found)
        self.assertEqual(checked['images'][0], 'https://rvb-img.fixtures.invalid/i/full-1.jpg')
        self.assertEqual(found['images'], checked['images'])
        # A recheck that sees no photos keeps the ones the listing has.
        bare = dict(REVERB_ITEM, photos=[], _links={'web': REVERB_ITEM['_links']['web']})
        kept = runner.recheck(dict(listing, images=['https://shop.test/old.jpg']), None, PROFILE, get_api=lambda u, h=None: json.dumps(bare))
        self.assertEqual(kept['images'], ['https://shop.test/old.jpg'])
        # A removed lead still picks up its photos.
        lead = dict(listing, verificationState='candidate')
        ended = dict(REVERB_ITEM, state={'slug': 'ended'})
        self.assertEqual(runner.recheck(lead, None, PROFILE, get_api=lambda u, h=None: json.dumps(ended))['images'][0], 'https://rvb-img.fixtures.invalid/i/full-1.jpg')

    def test_page_recheck_fills_photos_and_reports_them_when_the_offer_does_not_verify(self):
        listing = self.tracked()
        no_image = copy.deepcopy(PRODUCT); del no_image['image']
        markup = page(no_image) + '<meta property="og:image" content="https://shop.test/og-horn.jpg">'
        self.assertEqual(runner.recheck(listing, lambda _: markup, PROFILE)['images'], ['https://shop.test/og-horn.jpg'])
        found = {}
        unverified = '<title>Taylor horn</title><meta property="og:image" content="https://shop.test/og-horn.jpg">'
        self.assertIsNone(runner.recheck(listing, lambda _: unverified, PROFILE, found=found))
        self.assertEqual(found['images'], ['https://shop.test/og-horn.jpg'])
        # Existing photos are not replaced by a page that shows none.
        self.assertEqual(runner.recheck(dict(listing, images=['https://shop.test/old.jpg']), lambda _: page(no_image), PROFILE)['images'], ['https://shop.test/old.jpg'])

    def test_lead_hints_carry_the_page_photos(self):
        markup = '<title>Harrelson MUSE trumpet</title><meta property="og:image" content="https://shop.test/muse.jpg">'
        c = runner.candidate_hint({'url': 'https://shop.test/products/harrelson-muse-raw-brass-trumpet'}, 'Dealer', {'priorityMakers': ['Harrelson']}, markup)
        self.assertEqual(c['images'], ['https://shop.test/muse.jpg'])

    def test_daily_sends_photos_for_tracked_listings_that_have_none(self):
        from test_marketplaces import Client, MONETTE
        bare = self.tracked(url='https://shop.test/horn-bare', title='Taylor bare horn')
        pictured = self.tracked(url='https://shop.test/horn-pictured', title='Taylor pictured horn', id='22222222-2222-4222-8222-222222222222', images=['https://shop.test/has.jpg'])
        unverified = '<title>Taylor horn</title><meta property="og:image" content="https://shop.test/og-horn.jpg">'
        client = Client(baseline=[bare, pictured], due=[bare, pictured], profile=MONETTE)
        with patch.dict('os.environ', {'BRAVE_SEARCH_API_KEY': '', 'OPENAI_API_KEY': '', 'EBAY_CLIENT_ID': ''}), \
                patch.object(runner, 'reverb_search', return_value=[]), patch.object(runner, 'fetch', return_value=unverified), patch('builtins.print'):
            runner.daily(client)
        self.assertEqual(client.report['imageUpdates'], [{'id': bare['id'], 'images': ['https://shop.test/og-horn.jpg']}])
        self.assertEqual({f['id'] for f in client.report['checkFailures']}, {bare['id'], pictured['id']})

    def test_an_older_api_gets_the_run_without_image_updates(self):
        sent = []
        class OldApi:
            def call(self, path, body=None):
                sent.append(copy.deepcopy(body))
                if 'imageUpdates' in body:
                    raise urllib.error.HTTPError('https://api.fixture.invalid/runs', 400, 'Bad Request', {}, io.BytesIO(b'{"error":"invalid run JSON"}'))
                return {'status': body['status'], 'runId': 'saved'}
        report = {'externalId': 'x', 'status': 'succeeded', 'listings': [], 'imageUpdates': [{'id': 'a', 'images': ['https://shop.test/a.jpg']}]}
        with patch('builtins.print'):
            self.assertEqual(runner.submit_run(OldApi(), report)['runId'], 'saved')
        self.assertNotIn('imageUpdates', sent[1])


class BackfillTests(unittest.TestCase):
    IDS = {name: f'{i:08d}-0000-4000-8000-000000000000' for i, name in enumerate(['reverb', 'og', 'shopify', 'blocked1', 'blocked2', 'ebay', 'pictured', 'gone', 'nothing', 'nourl'])}

    def board(self):
        def l(name, url, images=()):
            return {'id': self.IDS[name], 'url': url, 'title': 'Fixture ' + name, 'images': list(images)}
        return [l('reverb', 'https://reverb.com/item/900000201-fixture'), l('og', 'https://dealer.test/horns/summit'),
                l('shopify', 'https://shop.test/products/taylor-chicago'), l('blocked1', 'https://blocked.test/a'), l('blocked2', 'https://blocked.test/b'),
                l('ebay', 'https://www.ebay.com/itm/900000000003'), l('pictured', 'https://dealer.test/horns/pictured', ['https://dealer.test/has.jpg']),
                l('gone', 'https://dealer.test/horns/gone'), l('nothing', 'https://dealer.test/horns/text-only'), dict(l('nourl', ''), url='')]

    def client(self):
        class Api:
            def __init__(s): s.posts = []
            def call(s, path, body=None):
                if path == '/listings' and body is None: return {'listings': self.board()}
                if path == '/runs': s.posts.append(copy.deepcopy(body)); return {'runId': 'r1', 'status': 'succeeded', 'imagesAdded': len(body['imageUpdates'])}
                raise AssertionError(path)
        return Api()

    def pages(self, seen):
        def get(url):
            seen.append(url)
            if url == 'https://dealer.test/horns/summit': return OG_PAGE
            if url == 'https://shop.test/products/taylor-chicago': return '<title>Taylor</title>'
            if url == 'https://shop.test/products/taylor-chicago.js': return json.dumps({'images': ['//shop.test/cdn/shop/files/horn_1600x.jpg?v=1']})
            if url.startswith('https://blocked.test/'): raise urllib.error.HTTPError(url, 403, 'Forbidden', {}, None)
            if url == 'https://dealer.test/horns/gone': raise urllib.error.HTTPError(url, 404, 'Not Found', {}, None)
            if url == 'https://dealer.test/horns/text-only': return '<title>Text only</title><p>Fixture</p>'
            raise AssertionError('unexpected fetch ' + url)
        return get

    def run_backfill(self, **kw):
        api, seen, api_calls, printed = self.client(), [], [], []
        def reverb(url, headers=None, *_):
            api_calls.append(url); return json.dumps(REVERB_ITEM)
        with patch('builtins.print', side_effect=lambda *a, **k: printed.append(' '.join(map(str, a)))):
            code = runner.backfill_images(api, get_page=self.pages(seen), get_api=reverb, sleep=lambda s: None, **kw)
        return code, api, seen, api_calls, printed

    def test_reverb_api_then_page_then_shopify_json_submitted_as_an_images_run(self):
        code, api, seen, api_calls, printed = self.run_backfill()
        self.assertEqual(code, 0)
        self.assertEqual(api_calls, [runner.REVERB_API + '900000201'])
        self.assertEqual(len(api.posts), 1)
        report = api.posts[0]
        self.assertEqual((report['kind'], report['status'], report['listings'], report['sources']), ('images', 'succeeded', [], []))
        self.assertTrue(report['externalId'].startswith('image-backfill-'))
        updates = {u['id']: u['images'] for u in report['imageUpdates']}
        self.assertEqual(set(updates), {self.IDS['reverb'], self.IDS['og'], self.IDS['shopify']})
        self.assertEqual(updates[self.IDS['reverb']][0], 'https://rvb-img.fixtures.invalid/i/full-1.jpg')
        self.assertEqual(updates[self.IDS['og']][0], 'https://dealer.test/uploads/summit-1_w2560.jpeg')
        self.assertEqual(updates[self.IDS['shopify']], ['https://shop.test/cdn/shop/files/horn_1600x.jpg?v=1'])
        # A refusing domain is tried once; eBay pages are never fetched; listings
        # that already have photos are left alone.
        self.assertEqual([u for u in seen if 'blocked.test' in u], ['https://blocked.test/a'])
        self.assertFalse(any('ebay' in u or 'pictured' in u for u in seen))
        # Counts only: no URLs, titles or ids in the log.
        log = '\n'.join(printed)
        for secret in ['http', 'Fixture', self.IDS['og']]:
            self.assertNotIn(secret, log)
        summary = json.loads(printed[0])
        self.assertEqual((summary['missing'], summary['found (reverb)'], summary['found (page)'], summary['blocked'], summary['gone'], summary['no photos (page)']), (8, 1, 2, 1, 1, 1))
        self.assertEqual(summary['skipped (eBay without API keys)'], 1)
        self.assertEqual(summary['skipped (source refused automated requests)'], 1)

    def test_request_caps(self):
        code, api, seen, api_calls, printed = self.run_backfill(limits={'reverb': 0, 'pages': 1})
        self.assertEqual(api_calls, [])
        self.assertEqual(seen, ['https://dealer.test/horns/summit'])
        self.assertEqual([u['id'] for u in api.posts[0]['imageUpdates']], [self.IDS['og']])
        self.assertGreaterEqual(json.loads(printed[0])['skipped (request cap)'], 5)
        _, api, seen, _, _ = self.run_backfill(limits={'perDomain': 1})
        self.assertEqual([u for u in seen if 'dealer.test' in u], ['https://dealer.test/horns/summit'])

    def test_ebay_photos_come_only_from_the_browse_api(self):
        class Ebay:
            calls = []
            def item(self, legacy_id):
                self.calls.append(legacy_id)
                return {'price': {'value': '2500.00', 'currency': 'USD'}, 'image': {'imageUrl': 'https://i.ebayimg.fixtures.invalid/images/g/x/s-l500.jpg'}, 'legacyItemId': legacy_id}
        _, api, seen, _, _ = self.run_backfill(ebay=Ebay())
        updates = {u['id']: u['images'] for u in api.posts[0]['imageUpdates']}
        self.assertEqual(Ebay.calls, ['900000000003'])
        self.assertEqual(updates[self.IDS['ebay']], ['https://i.ebayimg.fixtures.invalid/images/g/x/s-l500.jpg'])

    def test_a_refusing_api_is_not_called_again(self):
        # Review fix: after a Reverb 429 (or 403) the rest of the Reverb listings
        # are skipped, not retried one by one; the same for eBay.
        board = [{'id': f'{i:08d}-0000-4000-8000-000000000001', 'url': f'https://reverb.com/item/9000{i:05d}-fixture', 'images': []} for i in range(50)]
        board += [{'id': f'{i:08d}-0000-4000-8000-000000000002', 'url': f'https://www.ebay.com/itm/9000000{i:05d}', 'images': []} for i in range(20)]
        board.append({'id': '00000099-0000-4000-8000-000000000003', 'url': 'https://dealer.test/horns/summit', 'images': []})
        class Api:
            posts = []
            def call(s, path, body=None):
                if path == '/listings': return {'listings': board}
                s.posts.append(body); return {'runId': 'r', 'status': 'succeeded'}
        reverb_calls = []
        def always_429(url, headers=None, *_):
            reverb_calls.append(url); raise urllib.error.HTTPError(url, 429, 'Too Many Requests', {}, None)
        class Ebay:
            calls = 0
            def item(self, legacy_id):
                Ebay.calls += 1; raise urllib.error.HTTPError('https://api.ebay.fixtures.invalid', 403, 'Forbidden', {}, None)
        printed = []
        with patch('builtins.print', side_effect=lambda *a, **k: printed.append(' '.join(map(str, a)))):
            runner.backfill_images(Api(), get_page=lambda u: OG_PAGE, get_api=always_429, ebay=Ebay(), sleep=lambda s: None)
        self.assertEqual(set(reverb_calls), {reverb_calls[0]}, 'no further Reverb calls after the first refusal')
        self.assertLessEqual(len(reverb_calls), 3)  # the first listing's bounded backoff only
        self.assertEqual(Ebay.calls, 1, 'no further eBay calls after the first refusal')
        summary = json.loads(printed[0])
        self.assertEqual((summary['blocked'], summary['skipped (source refused automated requests)']), (2, 49 + 19))
        # A refusing API never stops page fetches on other domains.
        self.assertEqual([u['id'] for u in Api.posts[0]['imageUpdates']], ['00000099-0000-4000-8000-000000000003'])

    def test_nothing_found_submits_nothing(self):
        class Empty:
            posts = []
            def call(self, path, body=None):
                if path == '/listings': return {'listings': [{'id': 'a', 'url': 'https://www.ebay.com/itm/900000000003', 'images': []}]}
                self.posts.append(body)
        with patch('builtins.print'):
            self.assertEqual(runner.backfill_images(Empty(), get_page=self.pages([])), 0)
        self.assertEqual(Empty.posts, [])

    def test_cli_flag(self):
        with patch.dict('os.environ', {'TRUMPETS_API_URL': 'https://api.fixture.invalid', 'TRUMPETS_MACHINE_TOKEN': 'x', 'EBAY_CLIENT_ID': ''}), \
                patch('sys.argv', ['runner.py', '--backfill-images']), patch.object(runner, 'backfill_images', return_value=0) as backfill:
            self.assertEqual(runner.run(), 0)
        self.assertIsNone(backfill.call_args.kwargs['ebay'])


if __name__ == '__main__':
    unittest.main()
