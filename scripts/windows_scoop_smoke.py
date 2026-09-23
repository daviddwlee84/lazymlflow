#!/usr/bin/env python3
"""Exercise real Scoop handoff in disposable hosted Windows runner state."""
import argparse
import datetime
import functools
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time
import zipfile

SCOOP_SHA = 'b588a06e41d920d2123ec70aee682bae14935939'
p = argparse.ArgumentParser()
p.add_argument('--project', required=True)
p.add_argument('--binary', required=True)
p.add_argument('--main', required=True)
a = p.parse_args()
if os.name != 'nt' or os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('RUNNER_ENVIRONMENT') != 'github-hosted':
    raise SystemExit('This acceptance test requires a disposable GitHub-hosted Windows runner.')
repo = Path.cwd()
go, git, pwsh = [shutil.which(name) for name in ('go','git','pwsh')]
assert all((go, git, pwsh))
records=[]
def run(argv, env=None, **kwargs):
    r=subprocess.run(argv, env=env, capture_output=True, text=True, timeout=180, **kwargs)
    if r.returncode: raise AssertionError((argv,r.returncode,r.stdout,r.stderr))
    return r.stdout

def decode(text):
    d=json.loads(text)
    return d.get('data', d)

with tempfile.TemporaryDirectory(prefix='scoop handoff ') as scratch:
    root=Path(scratch).resolve(); assets=root/'assets';assets.mkdir()
    binaries={}
    for v in ['v0.0.1','v0.0.2']:
        binary=root/(a.binary+'-'+v+'.exe')
        flags='-X main.version='+v+' -X github.com/daviddwlee84/'+a.project+'/internal/cli.Version='+v
        run([go,'build','-ldflags',flags,'-o',str(binary),a.main],cwd=repo)
        assert v in run([str(binary),'--version'])
        binaries[v]=binary
    handler=functools.partial(http.server.SimpleHTTPRequestHandler,directory=str(assets))
    server=http.server.ThreadingHTTPServer(('127.0.0.1',0),handler)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    try:
        home=root/'home';home.mkdir();scoop=root/'custom scoop';manager=scoop/'apps/scoop/current'
        run([git,'init',str(manager)])
        run([git,'-C',str(manager),'remote','add','origin','https://github.com/ScoopInstaller/Scoop'])
        run([git,'-C',str(manager),'fetch','--depth','1','origin',SCOOP_SHA])
        run([git,'-C',str(manager),'checkout','--detach','FETCH_HEAD'])
        env=dict(os.environ,HOME=str(home),USERPROFILE=str(home),SCOOP=str(scoop),SCOOP_GLOBAL=str(root/'global-not-used'),XDG_CONFIG_HOME=str(home/'config'),XDG_DATA_HOME=str(home/'data'),XDG_CACHE_HOME=str(home/'cache'),XDG_STATE_HOME=str(home/'state'))
        for key in ['HTTP_PROXY','HTTPS_PROXY','ALL_PROXY','http_proxy','https_proxy','all_proxy']: env.pop(key,None)
        env['PATH']=str(scoop/'shims')+os.pathsep+env['PATH']
        config=home/'config/scoop/config.json';config.parent.mkdir(parents=True)
        config.write_text(json.dumps({'last_update':datetime.datetime.now().isoformat(),'aria2-enabled':False,'show_update_log':False,'scoop_branch':'master'}))
        bucket=scoop/'buckets/fixture/bucket';bucket.mkdir(parents=True)
        package='installed-'+a.project
        manifest=bucket/(package+'.json')
        def set_manifest(v, bad_hash=False, advertised=None):
            archive=assets/(a.binary+'-'+v+'.zip')
            with zipfile.ZipFile(archive,'w',zipfile.ZIP_DEFLATED) as z: z.write(binaries[v],a.binary+'.exe')
            digest=hashlib.sha256(archive.read_bytes()).hexdigest()
            manifest.write_text(json.dumps({'version':advertised or v[1:],'description':'isolated manager acceptance','homepage':'https://example.invalid','license':'MIT','architecture':{'64bit':{'url':f'http://127.0.0.1:{server.server_port}/{archive.name}','hash':'0'*64 if bad_hash else digest}},'bin':a.binary+'.exe'}))
        scoop_cmd=[pwsh,'-NoLogo','-NoProfile','-File',str(manager/'bin/scoop.ps1')]
        set_manifest('v0.0.1')
        run(scoop_cmd+['install','fixture/'+package],env=env)
        exe=scoop/'apps'/package/'current'/(a.binary+'.exe')
        old=exe.resolve();old_hash=hashlib.sha256(old.read_bytes()).hexdigest()
        print(json.dumps({'fixture_executable':str(exe),'resolved':str(old),'version':run([str(exe),'--version'],env=env),'powershell':pwsh,'receipt':json.loads((old.parent/'install.json').read_text(encoding='utf-8-sig'))}),flush=True)
        check=decode(run([str(exe),'upgrade','--check','--json'],env=env))
        assert check['package']==package and check['bucket']=='fixture' and check['can_upgrade'],check
        assert not (home/'state'/a.binary/'upgrades').exists(),'check wrote operation state'
        assert hashlib.sha256(old.read_bytes()).hexdigest()==old_hash
        records.append({'case':'readonly-check','status':'passed','package':package})
        def apply(expected):
            args=[str(exe),'upgrade','--json']+([] if a.binary=='lazyclash' else ['--yes'])
            initial=decode(run(args,env=env))
            assert initial['status']=='handed-off',initial
            deadline=time.monotonic()+120
            while time.monotonic()<deadline:
                queried=subprocess.run(initial['status_command'],env=env,capture_output=True,text=True,timeout=30)
                state=decode(queried.stdout)
                assert queried.returncode==0 or (a.project=='exp-cli' and queried.returncode==1 and state.get('status') in ('blocked','failed','canceled','interrupted')),(queried.returncode,queried.stdout,queried.stderr)
                if state['status'] in ['updated','up-to-date','blocked','failed','canceled','interrupted']:
                    if state['status']!=expected:
                        log=Path(state['log_path'])
                        raise AssertionError((expected,state,log.read_text(errors='replace') if log.exists() else 'no log'))
                    assert state['change_known'] == (expected in ('updated','up-to-date')),state
                    records.append({'case':expected,'operation_id':initial['operation_id'],'status':state['status'],'version':state.get('version')})
                    return state
                time.sleep(.2)
            raise AssertionError(('helper timed out',initial))
        set_manifest('v0.0.2')
        state=apply('updated');assert state['version']=='v0.0.2',state
        assert 'v0.0.2' in run([str(exe),'--version'],env=env)
        operation=Path(state['result_path']).parent
        request=operation/'request.json'
        before_result=Path(state['result_path']).read_bytes()
        replay=subprocess.run([str(operation/'helper.exe'),'--internal-scoop-upgrade',str(request),hashlib.sha256(request.read_bytes()).hexdigest(),state['operation_id']],env=env,capture_output=True,text=True,timeout=15)
        assert replay.returncode!=0 and Path(state['result_path']).read_bytes()==before_result,'helper request was replayable'
        records.append({'case':'completed-request-replay','status':'refused-without-changing-result'})
        # A suspended fixture executable is a real loaded image owned by this
        # test. No existing user process is discovered or stopped.
        held=subprocess.Popen([str(exe),'--version'],env=env,creationflags=0x4,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        try: apply('blocked')
        finally: held.kill();held.wait(timeout=10)
        apply('up-to-date')
        set_manifest('v0.0.2',bad_hash=True,advertised='0.0.3')
        apply('failed')
        assert 'v0.0.2' in run([str(exe),'--version'],env=env), 'checksum failure changed the installed version'
        assert hashlib.sha256(old.read_bytes()).hexdigest()==old_hash,'old version payload was unexpectedly overwritten'
        output=repo/'build/windows-scoop-smoke.json';output.parent.mkdir(exist_ok=True)
        output.write_text(json.dumps({'scoop_source':SCOOP_SHA,'project':a.project,'cases':records},indent=2))
        print(output.read_text())
    finally:server.shutdown();server.server_close()
