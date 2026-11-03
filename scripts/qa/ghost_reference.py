# /// script
# requires-python = ">=3.11"
# dependencies = ["wreq==0.13.0", "loguru==0.7.3"]
# ///
"""Run the pinned official HttpTaskStep unchanged with headless app adapters.

The only adapters are settings, desktop task metadata, file I/O, client creation
(fixed CDN IP, certificate verification enabled) and unlimited speed callbacks.
No changes are made to the official splitting/reassignment/acceleration code.
"""
import asyncio, dataclasses, hashlib, json, os, time
from pathlib import Path
from types import SimpleNamespace
from datetime import timedelta
from ipaddress import ip_address
from urllib.parse import urlsplit
from urllib.request import urlopen
from wreq import Client, Emulation
from wreq.emulation import Platform, Profile
from wreq.dns import DnsOptions
from loguru import logger

record=json.loads(Path(os.environ.get('BBDOWN_REFERENCE_INPUT','/tmp/bbdown-transport-reference.json')).read_text())
page=record['files'][0]
out=Path(os.environ['BBDOWN_REFERENCE_DIR'])
out.mkdir(parents=True,exist_ok=True)
logger.remove()  # Upstream exception messages may include signed URLs.

@dataclasses.dataclass(kw_only=True,eq=False)
class Task: pass
@dataclasses.dataclass(kw_only=True)
class TaskStep:
    task: object=None
    speed: int=0
    receivedBytes: int=0
    progress: float=0
    def setStatus(self,status): pass
class TaskError(Exception): pass
TaskStatus=SimpleNamespace(PAUSED='paused')
SpecialFileSize=SimpleNamespace(UNKNOWN=-1,NOT_SUPPORTED=-2)
cfg=SimpleNamespace(**{k:SimpleNamespace(value=v) for k,v in {
    'maxReassignSize':512,'autoSpeedUp':True,'clientProfile':'chrome',
    'shouldPreserveLastModified':False}.items()})
ftruncate=os.ftruncate
pwrite=os.pwrite
def toEmulation(*args):
    return Emulation(profile=Profile.Chrome154,platform=Platform.MacOS)
def buildClient(*,emulation=None,userAgent=None,readTimeout=30,**kw):
    dns=DnsOptions(system_dns=True)
    if record.get('dialIP'):
        dns.add_resolve(urlsplit(page['videoUrl']).hostname,[ip_address(record['dialIP'])])
    kw={'emulation':emulation,'dns_options':dns,'tls_verify':True,'read_timeout':timedelta(seconds=readTimeout)}
    if userAgent: kw['headers']={'user-agent':userAgent}
    return Client(**kw)

COMMIT='256b082166fec444b517dd3f4bb88c55ead1325b'
sourcePath=os.environ.get('BBDOWN_GHOST_HTTP_SOURCE')
if sourcePath:
    raw=Path(sourcePath).read_bytes()
else:
    with urlopen(f'https://raw.githubusercontent.com/XiaoYouChR/Ghost-Downloader-3/{COMMIT}/features/http_pack/task.py',timeout=30) as response:
        raw=response.read()
if hashlib.sha256(raw.rstrip()+b'\n').hexdigest()!='c8dc06dce5debfe4dedace56f485f08fae2a37b2e2d3336de7d84737e1bafead':
    raise RuntimeError('pinned official source checksum mismatch')
source=raw.decode()
source='\n'.join(l for l in source.splitlines() if not l.startswith('from app.'))
exec(compile(source,'pinned_official_http_task.py','exec'),globals())

async def main():
    # Discover the verified length without consuming the whole body.
    client=buildClient(emulation=toEmulation())
    response=await client.get(page['videoUrl'],headers={**page['headers'],'range':'bytes=0-0','accept-encoding':'identity'})
    total=int(bytes(response.headers.get('content-range')).decode().split('/')[-1])
    response.close();client.close()
    step=HttpTaskStep(task=SimpleNamespace(outputPath=str(out/'ghost-reference-video.m4s')),url=page['videoUrl'],fileSize=total,headers=page['headers'],subworkerCount=8,canUseRangeRequests=True)
    received=0;first=None;last=None;peak=0
    samples=[]
    started=time.monotonic()
    def report(n):
        nonlocal received,first,last
        now=time.monotonic()
        if first is None:first=now
        last=now;received+=n
    async def unlimited(): pass
    async def monitor():
        previous=0
        while True:
            await asyncio.sleep(1)
            samples.append({'seconds':time.monotonic()-started,'bytes':received,'speed':received-previous,'workers':len(step.subworkers)})
            previous=received
    monitorTask=asyncio.create_task(monitor())
    try: await asyncio.wait_for(step.run(report,unlimited),240)
    finally:
        monitorTask.cancel()
        try: await monitorTask
        except asyncio.CancelledError: pass
    data=Path(step.outputPath).read_bytes()
    result={'source_commit':COMMIT,'host':urlsplit(page['videoUrl']).hostname,'dialIP':record.get('dialIP'),'bytes':len(data),'reported_bytes':received,'first_payload_seconds':first-started,'elapsed_seconds':time.monotonic()-started,'transfer_KiB_s':received/(last-first)/1024,'sha256':hashlib.sha256(data).hexdigest(),'samples':samples}
    (out/'ghost-reference.json').write_text(json.dumps(result,indent=2))
    print(json.dumps({k:v for k,v in result.items() if k!='samples'}),flush=True)
    if len(data)!=total:raise RuntimeError('length mismatch')

try: asyncio.run(main())
except Exception as e:
    print('Reference failed:',type(e).__name__,flush=True)
    raise SystemExit(1)
