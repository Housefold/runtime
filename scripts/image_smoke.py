#!/usr/bin/env python3
"""Supplementary disposable-container packaging check; never HAOS acceptance."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid


def docker(*args):
    return subprocess.check_output(['docker', '--host=unix:///var/run/docker.sock', *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--isolated-test', action='store_true', required=True)
    parser.add_argument('--image', default='housefold-runtime:agent-amd64')
    args = parser.parse_args()
    prefix = 'housefold-packaging-' + uuid.uuid4().hex[:12]
    net, volume, bad = prefix+'-net', prefix+'-data', prefix+'-bad'
    names = [prefix+'-normal', prefix+'-recovery']
    request = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        docker('network', 'create', net)
        docker('volume', 'create', volume)
        docker('volume', 'create', bad)
        with tempfile.TemporaryDirectory() as temp:
            sentinel = Path(temp)/'keep'
            sentinel.write_bytes(b'synthetic-preserved-state')
            for name, estate, code in [(names[0],volume,200),(names[1],bad,503)]:
                if estate == bad:
                    docker('run','--rm','--network','none','-v',bad+':/estate',
                           '--entrypoint','/bin/sh',
                           'public.ecr.aws/docker/library/golang:1.26.8-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c',
                           '-c','printf synthetic-preserved-state > /estate/housefold')
                docker('run','-d','--name',name,'--init','--network',net,
                       '--cap-drop','ALL','--cap-add','SETUID','--cap-add','SETGID',
                       '--cap-add','CHOWN','--cap-add','FOWNER','--cap-add','KILL',
                       '-v',estate+':/data','-p','127.0.0.1::8099',
                       '-e','SUPERVISOR_TOKEN=synthetic-token',args.image)
                inspection=json.loads(docker('inspect',name))[0]
                ports=inspection['NetworkSettings']['Ports']
                if '8099/tcp' not in ports:
                    raise AssertionError({'state':inspection['State'],'logs':docker('logs',name)})
                port=ports['8099/tcp'][0]['HostPort']
                url='http://127.0.0.1:'+port
                until=time.monotonic()+10
                while True:
                    try:
                        response=request.open(url+'/healthz',timeout=1)
                        observed=response.status
                        response.close()
                        break
                    except urllib.error.HTTPError as err:
                        observed=err.code
                        break
                    except urllib.error.URLError:
                        if time.monotonic()>=until:
                            raise
                        time.sleep(.05)
                assert observed==code,(observed,code)
                procs=docker('top',name,'-eo','uid,pid,args')
                assert any(line.split()[0]=='10001' and '/housefold-runtime' in line for line in procs.splitlines()[1:]),procs
                try:
                    request.open(url+'/',timeout=1)
                    raise AssertionError('direct ingress bypass')
                except urllib.error.HTTPError as err:
                    assert err.code==403
                if code==200:
                    docker('cp',str(sentinel),name+':/data/housefold/keep')
                    docker('restart','--time','10',name)
                    output=Path(temp)/'restored'
                    docker('cp',name+':/data/housefold/keep',str(output))
                    assert output.read_bytes()==sentinel.read_bytes()
                else:
                    output=Path(temp)/'bad-preserved'
                    docker('cp',name+':/data/housefold',str(output))
                    assert output.read_bytes()==sentinel.read_bytes()
                logs=docker('logs',name)
                assert 'synthetic-token' not in logs
                docker('stop','--time','10',name)
                state=json.loads(docker('inspect',name))[0]['State']
                assert state['ExitCode']==0,state
                print(json.dumps({'case':name.rsplit('-',1)[1],'health':code,'runtime_uid':10001,
                                  'shutdown_exit':state['ExitCode'],'state_preserved':True}))
        print('PASS: disposable-container packaging only; HAOS gate remains separate')
    finally:
        for name in names:
            subprocess.run(['docker','--host=unix:///var/run/docker.sock','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for vol in [volume,bad]:
            subprocess.run(['docker','--host=unix:///var/run/docker.sock','volume','rm',vol],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        subprocess.run(['docker','--host=unix:///var/run/docker.sock','network','rm',net],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)


if __name__=='__main__':
    main()
