import io,json,tempfile,unittest,threading,urllib.request,urllib.error
from pathlib import Path
from types import SimpleNamespace
from http.server import ThreadingHTTPServer
from library import Library,MAX_PDF,validate_resources
from hosted import handler

class LibraryTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name);self.lib=Library(self.root/'library')
 def test_upload_persists_deduplicates_and_rejects_incomplete(self):
  data=b'%PDF-1.7\nexample\n%%EOF';item=self.lib.save(io.BytesIO(data),len(data),'Book','Author')
  self.assertEqual(self.lib.save(io.BytesIO(data),len(data),'Duplicate')['id'],item['id'])
  self.assertEqual(Library(self.root/'library').items()[0]['title'],'Book')
  for content,size in [(b'not PDF',7),(data,len(data)+10),(data,MAX_PDF+1)]:
   with self.assertRaises(ValueError):self.lib.save(io.BytesIO(content),size,'bad')
  self.assertEqual(len(self.lib.items()),1);self.assertEqual(list(self.lib.root.glob('*.upload')),[])
 def test_resource_validation_is_optional_and_strict(self):
  self.assertEqual(validate_resources(None),[])
  value=[dict(title='Example Recording (1950)',artist='Example Artist',note='Example note.',url='https://example.com/listen')]
  self.assertEqual(validate_resources(value),value)
  for bad in [[dict(title='Bad',url='javascript:alert(1)')],[dict(title='Bad',url='https:relative')],[dict(title='Bad',url='https://user:pass@example.com')],[dict(title='',url='https://example.com')],['bad']]:
   with self.assertRaises(ValueError):validate_resources(bad)
 def test_private_routes_upload_and_pdf_ranges(self):
  server=ThreadingHTTPServer(('127.0.0.1',0),handler(SimpleNamespace(root=self.root,jobs={},active=None),'owner'))
  thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
  def req(path,body=None,headers=None,method=None):
   r=urllib.request.Request('http://127.0.0.1:'+str(server.server_port)+path,data=body,headers=headers or {},method=method)
   try:
    with urllib.request.urlopen(r) as response:return response.status,response.headers,response.read()
   except urllib.error.HTTPError as e:return e.code,e.headers,e.read()
  try:
   self.assertEqual(req('/library')[0],401)
   data=b'%PDF-1.7\nexample\n%%EOF'
   headers={'X-Jazz-User':'owner','Origin':'https://zachbednarke.com','Content-Type':'application/pdf'}
   self.assertEqual(req('/library?title=Book',data,{**headers,'Origin':'https://evil.test'})[0],403)
   code,_,body=req('/library?title=Book',data,headers);self.assertEqual(code,201);item=json.loads(body)
   resources=[dict(title='Example Recording (1950)',artist='Example Artist',note='Example note.',url='https://example.com/listen')]
   patch=json.dumps(dict(resources=resources)).encode();code,_,body=req('/library/'+item['id'],patch,{**headers,'Content-Type':'application/json'},'PATCH');self.assertEqual(code,200);self.assertEqual(json.loads(body)['resources'],resources)
   self.assertEqual(req('/library/'+item['id'],json.dumps(dict(resources=[dict(title='Bad',url='http://bad')])).encode(),{**headers,'Content-Type':'application/json'},'PATCH')[0],400)
   path='/library/'+item['id']+'/file'
   self.assertEqual(req(path)[0],401)
   code,h,body=req(path,headers={'X-Jazz-User':'owner','Range':'bytes=0-4'});self.assertEqual((code,body),(206,b'%PDF-'));self.assertEqual(h['Content-Range'],f'bytes 0-4/{len(data)}')
   self.assertEqual(req(path,headers={'X-Jazz-User':'owner','Range':'bytes=999-1000'})[0],416)
   self.assertEqual(req('/library/../../etc/passwd/file',headers=headers)[0],404)
  finally:server.shutdown();server.server_close();thread.join()
