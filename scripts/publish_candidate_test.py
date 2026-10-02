import copy, unittest
from unittest.mock import patch
import publish_candidate as publish
import release_candidate as release

class PublishingTests(unittest.TestCase):
    def test_current_blocked_harness_rejects_publication_before_network(self):
        for stage in (True,False):
            with self.assertRaises(ValueError):publish.check_ledger(stage)
    def test_stage_requires_measured_security_soak_and_promotion_adds_haos(self):
        receipt={'source':'a'*40,'version':'1.0.0','artifacts':{'runtime-amd64':'sha256:'+'b'*64},'image_manifests':{'amd64':'sha256:'+'c'*64,'aarch64':'sha256:'+'d'*64},'catalog_authority':{'public_key':'e'*64}}
        gates={kind:dict(gate=kind,result='pass',**{key:receipt[key] for key in ('source','artifacts','image_manifests')}) for kind in ('security','soak')}
        publish.check_gates(receipt,gates,True)
        with self.assertRaises(KeyError):publish.check_gates(receipt,gates,False)
        changed=copy.deepcopy(gates);changed['soak']['source']='f'*40
        with self.assertRaises(ValueError):publish.check_gates(receipt,changed,True)
    def test_registry_conflict_and_missing_candidate_never_overwrite(self):
        receipt={'version':'1.0.0','image_manifests':{'amd64':'sha256:'+'a'*64,'aarch64':'sha256:'+'b'*64}}
        with patch.object(publish,'inspect',return_value='sha256:'+'c'*64),patch.object(publish.subprocess,'run') as run:
            with self.assertRaises(ValueError):publish.push_images(__import__('pathlib').Path('/tmp'),receipt,True)
            run.assert_not_called()
        with patch.object(publish,'inspect',return_value=None),patch.object(publish.subprocess,'run') as run:
            with self.assertRaises(ValueError):publish.push_images(__import__('pathlib').Path('/tmp'),receipt,False)
            run.assert_not_called()
