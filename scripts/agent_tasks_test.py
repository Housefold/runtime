import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

class TaskLedgerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        self.docs = self.root / 'docs/agent'
        (self.docs / 'evidence').mkdir(parents=True)
        shutil.copyfile(Path(__file__).with_name('agent_tasks.py'), self.root / 'scripts/agent_tasks.py')
        (self.docs / 'evidence/result.md').write_text('Actual checks and waived criteria.\n')
        self.row = dict(id='ENV', title='Environment', depends_on=[], kind='environment',
                        status='blocked', approval_required=None, evidence=None, blocker='No hardware')
        self.save()

    def save(self):
        (self.docs / 'tasks.json').write_text(json.dumps(dict(schema_version=1, tasks=[self.row])))

    def run_cli(self, *args):
        return subprocess.run(['python3', str(self.root / 'scripts/agent_tasks.py'), *args],
                              text=True, capture_output=True)

    def test_waiver_requires_active_task_and_reviewable_evidence(self):
        self.assertNotEqual(self.run_cli('waive', 'ENV', '--reason', 'Explicit stakeholder waiver',
                                        '--evidence', 'docs/agent/evidence/result.md').returncode, 0)
        self.assertEqual(self.run_cli('resume', 'ENV').returncode, 0)
        self.assertEqual(self.run_cli('start', 'ENV').returncode, 0)
        self.assertNotEqual(self.run_cli('waive', 'ENV', '--evidence', 'docs/agent/evidence/result.md').returncode, 0)
        self.assertNotEqual(self.run_cli('waive', 'ENV', '--reason', 'Explicit waiver',
                                        '--evidence', 'docs/agent/evidence/missing.md').returncode, 0)
        self.assertEqual(self.run_cli('waive', 'ENV', '--reason', 'Explicit stakeholder waiver',
                                     '--evidence', 'docs/agent/evidence/result.md').returncode, 0)
        row = json.loads((self.docs / 'tasks.json').read_text())['tasks'][0]
        self.assertEqual(row['status'], 'waived')
        self.assertEqual(row['waiver'], 'Explicit stakeholder waiver')
        self.assertIsNone(row['blocker'])
        self.assertIn('No ready task', self.run_cli('next').stdout)
        self.assertNotEqual(self.run_cli('done', 'ENV', '--evidence', 'docs/agent/evidence/result.md').returncode, 0)

    def test_waiver_cannot_replace_implementation_or_approval(self):
        for kind, approval in [('implementation', None), ('environment', 'maintainer')]:
            self.row.update(kind=kind, approval_required=approval, status='in_progress')
            self.save()
            self.assertNotEqual(self.run_cli('waive', 'ENV', '--reason', 'Explicit waiver',
                                            '--evidence', 'docs/agent/evidence/result.md').returncode, 0)

if __name__ == '__main__':
    unittest.main()
