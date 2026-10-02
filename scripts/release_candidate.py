#!/usr/bin/env python3
"""Prepare source-traceable offline candidates. Never pushes or publishes."""
import argparse, hashlib, json, os, re, subprocess, tempfile, tarfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ARCHES = {"amd64": "amd64", "aarch64": "arm64"}
SHA = re.compile(r"[a-f0-9]{40}")
VERSION = re.compile(r"1\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?")

def run(argv, cwd=None, env=None):
    return subprocess.check_output(argv, cwd=cwd, env=env, text=True).strip()

def digest(path):
    h=hashlib.sha256()
    with path.open("rb") as file:
        for block in iter(lambda:file.read(1024*1024), b""): h.update(block)
    return h.hexdigest()

def replace_scalar(text, name, value):
    pattern=rf"(?m)^{re.escape(name)}:.*$"
    if len(re.findall(pattern,text))!=1: raise ValueError("ambiguous App metadata")
    return re.sub(pattern,lambda _:f"{name}: {json.dumps(value)}",text)

def repository(config,version,image):
    if not VERSION.fullmatch(version) or not re.fullmatch(r"ghcr\.io/housefold/runtime-\{arch\}",image):
        raise ValueError("invalid official distribution reference")
    if re.search(r"(?m)^image:",config): raise ValueError("source config already selects an image")
    return replace_scalar(config,"version",version)+f"\nimage: {json.dumps(image)}\n"

def extract(source, dest):
    """Read only Git-owned, link-free regular source, never the mutable workspace."""
    archive = dest.parent / "source.tar"
    subprocess.run(["git","archive","--format=tar","-o",str(archive),source],cwd=ROOT,check=True)
    with tarfile.open(archive) as tar:
        for member in tar.getmembers():
            path=Path(member.name)
            if path.is_absolute() or ".." in path.parts or not(member.isfile() or member.isdir()):
                raise ValueError("unsafe source tree")
        tar.extractall(dest,filter="data")

def prepare(source,out,version,images=False):
    if not SHA.fullmatch(source) or not VERSION.fullmatch(version): raise ValueError("full source commit and v1 version required")
    resolved=run(["git","rev-parse",f"{source}^{{commit}}"],ROOT)
    if resolved!=source: raise ValueError("source identity mismatch")
    if out.exists() or out.is_symlink(): raise ValueError("output already exists")
    out.mkdir(parents=False)
    epoch=run(["git","show","-s","--format=%ct",source],ROOT)
    receipt={"schema":1,"source":source,"version":version,"source_date_epoch":int(epoch),"runtime_major":1,"protocol_major":1,"artifacts":{},"image_manifests":{},"release_validated":False}
    try:
        with tempfile.TemporaryDirectory(prefix="housefold-candidate-") as temp:
            src=Path(temp)/"src";src.mkdir();extract(source,src)
            toolchain=run(["go","version"])
            if "go1.26.8 " not in toolchain: raise ValueError("Go 1.26.8 required")
            receipt["toolchain"]=toolchain
            catalog_source=(src/"internal/catalog/catalog.go").read_text()
            key=re.search(r'var officialPublicKey = "([a-f0-9]*)"',catalog_source)
            receipt["catalog_authority"]={"key_id":"housefold-modules-v1","public_key":key.group(1) if key else "","url":"https://housefold.github.io/modules/v1/"}
            for arch,goarch in ARCHES.items():
                env=dict(os.environ,CGO_ENABLED="0",GOOS="linux",GOARCH=goarch,SOURCE_DATE_EPOCH=epoch,GOTOOLCHAIN="local",GOFLAGS="",GOWORK="off",GOENV="off")
                for command in ("runtime","module-launcher","reference-module"):
                    name=f"{command}-{arch}"
                    flags="-s -w"
                    if command=="runtime": flags+=f" -X main.buildVersion={version} -X main.buildSource={source}"
                    subprocess.run(["go","build","-trimpath","-buildvcs=false",f"-ldflags={flags}","-o",str(out/name),f"./cmd/{command}"],cwd=src,env=env,check=True)
                    receipt["artifacts"][name]="sha256:"+digest(out/name)
                if images:
                    name=f"runtime-{arch}.oci.tar"
                    args=["docker","buildx","build","--platform",f"linux/{goarch}","--provenance=false","--sbom=false","--build-arg",f"BUILD_ARCH={arch}","--build-arg",f"BUILD_VERSION={version}","--build-arg",f"BUILD_SOURCE={source}","--build-arg",f"SOURCE_DATE_EPOCH={epoch}","--output",f"type=oci,dest={out/name},rewrite-timestamp=true",str(src)]
                    if os.getenv("HOUSEFOLD_BUILD_CA"): args += ["--secret",f"id=proxy_ca,src={os.environ['HOUSEFOLD_BUILD_CA']}"]
                    subprocess.run(args,check=True)
                    receipt["artifacts"][name]="sha256:"+digest(out/name)
                    receipt["image_manifests"][arch]=oci_manifest(out/name,arch,source,version)
            repo=out/"repository";app=repo/"housefold_runtime";app.mkdir(parents=True)
            # Existing Housefold/runtime can be an actual App Git repository;
            # an immutable candidate branch must contain ONLY this generated tree.
            (repo/"repository.yaml").write_text('name: Housefold Home Assistant Apps\nurl: https://github.com/Housefold/runtime\nmaintainer: Housefold\n')
            (app/"config.yaml").write_text(repository((src/"config.yaml").read_text(),version,"ghcr.io/housefold/runtime-{arch}"))
            (app/"DOCS.md").write_text("Housefold Runtime candidate. Synthetic disposable HAOS testing only.\nNormal HA App lifecycle, backup and logs. Production release gates remain mandatory.\n")
            for path in sorted(repo.rglob("*")):
                if path.is_file():receipt["artifacts"][str(path.relative_to(out))]="sha256:"+digest(path)
        (out/"candidate.json").write_text(json.dumps(receipt,sort_keys=True,indent=2)+"\n")
        return receipt
    except BaseException:
        # Preserve exact partial evidence rather than claiming a valid candidate.
        (out/"INCOMPLETE").write_text("Preparation failed. No candidate receipt is valid.\n")
        raise

def oci_manifest(path,arch,source=None,version=None):
    with tarfile.open(path) as tar:
        index=json.load(tar.extractfile("index.json"))
        manifests=index.get("manifests",[])
        if len(manifests)!=1:raise ValueError("ambiguous OCI candidate")
        item=manifests[0];value=item.get("digest","")
        if not re.fullmatch(r"sha256:[a-f0-9]{64}",value):raise ValueError("invalid OCI identity")
        raw=tar.extractfile("blobs/sha256/"+value[7:]).read()
        if hashlib.sha256(raw).hexdigest()!=value[7:]:raise ValueError("OCI manifest mismatch")
        manifest=json.loads(raw);config=manifest["config"]["digest"]
        conf_raw=tar.extractfile("blobs/sha256/"+config[7:]).read()
        if hashlib.sha256(conf_raw).hexdigest()!=config[7:]:raise ValueError("OCI config mismatch")
        conf=json.loads(conf_raw)
        if conf.get("os")!="linux" or conf.get("architecture")!=ARCHES[arch]:raise ValueError("OCI target mismatch")
        if source is not None:
            labels=conf.get("config",{}).get("Labels",{})
            if labels.get("org.opencontainers.image.revision")!=source or labels.get("org.opencontainers.image.version")!=version:raise ValueError("OCI source/version mismatch")
        return value

def verify(out, receipt):
    if (out/"INCOMPLETE").exists() or receipt.get("schema")!=1 or not SHA.fullmatch(receipt.get("source","")): raise ValueError("invalid candidate")
    actual=set()
    for path in out.rglob("*"):
        if path.is_symlink():raise ValueError("linked candidate tree")
        if path.is_file() and path.name!="candidate.json":actual.add(str(path.relative_to(out)))
    if actual!=set(receipt["artifacts"]):raise ValueError("candidate inventory changed")
    for name,value in receipt["artifacts"].items():
        path=Path(name)
        if path.is_absolute() or ".." in path.parts or path!=Path(os.path.normpath(name)):raise ValueError("invalid artifact path")
        at=out
        for part in path.parts:
            at=at/part
            if at.is_symlink():raise ValueError("linked artifact")
        if not at.is_file() or value!="sha256:"+digest(at):raise ValueError("artifact changed")

def promotion(receipt,gates):
    """Match measured gate receipts to the existing artifact; never rebuild."""
    if set(receipt.get("image_manifests",{}))!=set(ARCHES):raise ValueError("both exact architecture images required")
    authority=receipt.get("catalog_authority",{})
    if not re.fullmatch(r"[a-f0-9]{64}",authority.get("public_key","")):raise ValueError("official catalog signing authority required")
    for kind in ("security","soak","haos"):
        gate=gates[kind]
        if gate.get("gate")!=kind or gate.get("result")!="pass" or gate.get("source")!=receipt["source"] or gate.get("artifacts")!=receipt["artifacts"] or gate.get("image_manifests")!=receipt["image_manifests"]:
            raise ValueError(f"{kind} gate does not match exact artifact")
    haos=gates["haos"]
    if not haos.get("disposable") or not haos.get("repository_install") or not haos.get("haos_version") or not haos.get("core_version") or not haos.get("supervisor_version") or not haos.get("acceptance_matrix_complete"):
        raise ValueError("actual disposable HAOS acceptance evidence required")
    # Receipts record evidence, not an independent security authority. The release
    # job also requires ledger gates done, reviewed evidence and protected custody.
    return True

def main():
    p=argparse.ArgumentParser();p.add_argument("command",choices=["prepare","verify","check-promotion"]);p.add_argument("--source");p.add_argument("--out",type=Path,required=True);p.add_argument("--version");p.add_argument("--images",action="store_true");p.add_argument("--gates",type=Path)
    a=p.parse_args();out=a.out.resolve()
    if a.command=="prepare":prepare(a.source or "",out,a.version or "",a.images)
    else:
        receipt=json.loads((out/"candidate.json").read_text());verify(out,receipt)
        if a.command=="check-promotion":
            ledger=json.loads((ROOT/"docs/agent/tasks.json").read_text())
            rows={t["id"]:t for t in ledger["tasks"]}
            if any(rows[task]["status"]!="done" for task in ("V1P05","V1P11","V1P12","V1P13","V1P14")):raise ValueError("mandatory ledger gates incomplete")
            promotion(receipt,json.loads(a.gates.read_text()))

if __name__=="__main__":main()
