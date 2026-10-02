import copy, json, tempfile, unittest
from pathlib import Path
import release_candidate as release

class DistributionTests(unittest.TestCase):
    def test_repository_metadata_and_injection_rejection(self):
        config='name: Runtime\nversion: "0.1.0"\narch: [amd64, aarch64]\n'
        result=release.repository(config,'1.0.0-candidate.abc','ghcr.io/housefold/runtime-{arch}')
        self.assertIn('version: "1.0.0-candidate.abc"',result)
        self.assertIn('image: "ghcr.io/housefold/runtime-{arch}"',result)
        for version,image in [('1.0.0\nimage: evil','ghcr.io/housefold/runtime-{arch}'),('1.0.0','evil/registry')]:
            with self.assertRaises(ValueError):release.repository(config,version,image)
        with self.assertRaises(ValueError):release.repository(config+'version: evil\n','1.0.0','ghcr.io/housefold/runtime-{arch}')
    def test_receipt_detects_tamper_links_and_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);file=root/'runtime-amd64';file.write_bytes(b'SYNTHETIC')
            receipt={'schema':1,'source':'a'*40,'artifacts':{'runtime-amd64':'sha256:'+release.digest(file)}}
            release.verify(root,receipt)
            file.write_bytes(b'TAMPERED')
            with self.assertRaises(ValueError):release.verify(root,receipt)
            file.unlink();file.symlink_to('/etc/hostname')
            with self.assertRaises(ValueError):release.verify(root,receipt)
            receipt['artifacts']={'../foreign':'sha256:'+'a'*64}
            with self.assertRaises(ValueError):release.verify(root,receipt)
    def test_promotion_requires_same_source_exact_artifacts_all_real_gates(self):
        receipt={'source':'a'*40,'artifacts':{'runtime-amd64':'sha256:'+'b'*64},'image_manifests':{'amd64':'sha256:'+'c'*64,'aarch64':'sha256:'+'d'*64},'catalog_authority':{'public_key':'e'*64}}
        gates={kind:dict(gate=kind,result='pass',source=receipt['source'],artifacts=receipt['artifacts'],image_manifests=receipt['image_manifests'])for kind in ('security','soak','haos')}
        gates['haos'].update(disposable=True,repository_install=True,haos_version='synthetic',core_version='synthetic',supervisor_version='synthetic',acceptance_matrix_complete=True)
        self.assertTrue(release.promotion(receipt,gates)) # schema test only; does not create acceptance evidence
        for kind in gates:
            wrong=copy.deepcopy(gates);wrong[kind]['artifacts']['runtime-amd64']='sha256:'+'f'*64
            with self.assertRaises(ValueError):release.promotion(receipt,wrong)
        wrong=copy.deepcopy(gates);wrong['haos']['repository_install']=False
        with self.assertRaises(ValueError):release.promotion(receipt,wrong)
        receipt['catalog_authority']['public_key']=''
        with self.assertRaises(ValueError):release.promotion(receipt,gates)
    def test_extra_untracked_secret_is_not_published(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);file=root/'runtime-amd64';file.write_bytes(b'SYNTHETIC')
            receipt={'schema':1,'source':'a'*40,'artifacts':{'runtime-amd64':'sha256:'+release.digest(file)}}
            (root/'UNTRACKED_SECRET').write_text('PRIVATE')
            with self.assertRaises(ValueError):release.verify(root,receipt)
