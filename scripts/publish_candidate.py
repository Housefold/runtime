#!/usr/bin/env python3
"""Stage/promote existing verified OCI bytes. No rebuild, signing or live HAOS."""
import argparse, hashlib, json, os, shutil, subprocess, tempfile
from pathlib import Path
import release_candidate as release

REPO='https://github.com/Housefold/runtime.git'

def check_ledger(stage):
    rows={r['id']:r for r in json.loads((release.ROOT/'docs/agent/tasks.json').read_text())['tasks']}
    required=['V1P05','V1P11','V1P12','V1P13']
    if not stage:required.append('V1P14')
    if any(rows[t]['status']!='done' for t in required):raise ValueError('mandatory ledger gates incomplete')

def check_gates(receipt,gates,stage):
    if set(receipt.get('image_manifests',{}))!=set(release.ARCHES):raise ValueError('exact OCI images missing')
    if not release.VERSION.fullmatch(receipt.get('version','')):raise ValueError('invalid candidate version')
    if not release.SHA.fullmatch(receipt.get('source','')):raise ValueError('invalid source')
    if not __import__('re').fullmatch('[a-f0-9]{64}',receipt.get('catalog_authority',{}).get('public_key','')):raise ValueError('official authority unavailable')
    for kind in ('security','soak'):
        gate=gates[kind]
        if gate.get('gate')!=kind or gate.get('result')!='pass' or any(gate.get(key)!=receipt.get(key) for key in ('source','artifacts','image_manifests')):raise ValueError('gate does not match exact candidate')
    if not stage:release.promotion(receipt,gates)

def inspect(target):
    result=subprocess.run(['skopeo','inspect','--raw','docker://'+target],capture_output=True,timeout=60)
    if result.returncode:
        error=result.stderr.decode(errors='replace').lower()
        if 'manifest unknown' in error or 'name unknown' in error:return None
        raise ValueError('registry inspection unavailable; no upload attempted')
    return 'sha256:'+hashlib.sha256(result.stdout).hexdigest()

def push_images(out,receipt,stage):
    for arch in release.ARCHES:
        target=f"ghcr.io/housefold/runtime-{arch}:{receipt['version']}"
        expected=receipt['image_manifests'][arch]
        current=inspect(target)
        if current is not None and current!=expected:raise ValueError('immutable registry version conflict')
        if current is None:
            if not stage:raise ValueError('validated candidate missing; promotion never rebuilds/uploads a substitute')
            subprocess.run(['skopeo','copy','--preserve-digests','oci-archive:'+str(out/f'runtime-{arch}.oci.tar'),'docker://'+target],check=True,timeout=300)
        if inspect(target)!=expected:raise ValueError('registry changed exact candidate identity')

def publish_repository(out,receipt,stage):
    branch='candidate/'+receipt['source'] if stage else 'apps'
    env=dict(os.environ,GIT_TERMINAL_PROMPT='0')
    with tempfile.TemporaryDirectory(prefix='housefold-app-repo-') as temp:
        root=Path(temp)/'repo'
        subprocess.run(['git','clone','--no-checkout',REPO,str(root)],check=True,env=env,timeout=120)
        exists=subprocess.run(['git','show-ref','--verify','--quiet','refs/remotes/origin/'+branch],cwd=root,env=env).returncode==0
        if exists:
            subprocess.run(['git','checkout','-B',branch,'origin/'+branch],cwd=root,check=True,env=env)
            # Protect source-bearing branches. Only dedicated metadata trees can
            # be updated; never use force push or operate Runtime main here.
            tracked=release.run(['git','ls-files'],root).splitlines()
            allowed={'repository.yaml','housefold_runtime/config.yaml','housefold_runtime/DOCS.md'}
            if not set(tracked)<=allowed:raise ValueError('distribution branch contains unexpected/source files')
            subprocess.run(['git','rm','-r','--ignore-unmatch','.'],cwd=root,check=True,env=env)
        else:
            subprocess.run(['git','checkout','--orphan',branch],cwd=root,check=True,env=env)
            subprocess.run(['git','rm','-r','--ignore-unmatch','.'],cwd=root,check=True,env=env)
        shutil.copytree(out/'repository',root,dirs_exist_ok=True)
        subprocess.run(['git','add','repository.yaml','housefold_runtime'],cwd=root,check=True,env=env)
        if subprocess.run(['git','diff','--cached','--quiet'],cwd=root).returncode==0:return
        if stage and exists:raise ValueError('immutable candidate repository conflict')
        subprocess.run(['git','-c','user.name=Housefold Release','-c','user.email=release@housefold.invalid','commit','-m',f"{'Stage' if stage else 'Publish'} exact Runtime {receipt['version']} from {receipt['source']}"],cwd=root,check=True,env=env)
        subprocess.run(['git','-c','credential.helper=','-c','credential.helper=!gh auth git-credential','push','origin','HEAD:refs/heads/'+branch],cwd=root,check=True,env=env,timeout=120)

def main():
    p=argparse.ArgumentParser();p.add_argument('command',choices=['stage','promote']);p.add_argument('--out',type=Path,required=True);p.add_argument('--gates',type=Path,required=True)
    a=p.parse_args();stage=a.command=='stage';receipt=json.loads((a.out/'candidate.json').read_text());gates=json.loads(a.gates.read_text())
    release.verify(a.out,receipt);check_ledger(stage);check_gates(receipt,gates,stage)
    push_images(a.out,receipt,stage);publish_repository(a.out,receipt,stage)
    print(f"Exact artifact {'staged for disposable HAOS' if stage else 'promoted'}: https://github.com/Housefold/runtime#{'candidate/'+receipt['source'] if stage else 'apps'}")

if __name__=='__main__':main()
