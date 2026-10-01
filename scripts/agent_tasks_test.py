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
        (self.docs / 'evidence/result.md').write_text('Exact commands, results and acceptance evidence.\n')
        self.rows = [dict(id='FIRST', title='First', depends_on=[], kind='implementation',
                         status='todo', approval_required=None, evidence=None, blocker=None),
                     dict(id='SECOND', title='Second', depends_on=['FIRST'], kind='release_gate',
                         status='todo', approval_required=None, evidence=None, blocker=None)]
        self.save()

    def save(self):
        (self.docs / 'tasks.json').write_text(json.dumps(dict(schema_version=2, tasks=self.rows)))

    def run_cli(self, *args):
        return subprocess.run(['python3', str(self.root / 'scripts/agent_tasks.py'), *args],
                              text=True, capture_output=True)

    def current(self):
        return json.loads((self.docs / 'tasks.json').read_text())['tasks']

    def test_dependencies_single_owner_and_evidence(self):
        self.assertEqual(json.loads(self.run_cli('next').stdout)['id'], 'FIRST')
        self.assertNotEqual(self.run_cli('start', 'SECOND').returncode, 0)
        self.assertEqual(self.run_cli('start', 'FIRST').returncode, 0)
        self.assertNotEqual(self.run_cli('start', 'FIRST').returncode, 0)
        self.assertNotEqual(self.run_cli('start', 'SECOND').returncode, 0)
        self.assertNotEqual(self.run_cli('done', 'FIRST').returncode, 0)
        for path in ['docs/agent/evidence/missing.md', 'docs/agent/tasks.json', '../escape.md']:
            self.assertNotEqual(self.run_cli('done', 'FIRST', '--evidence', path).returncode, 0)
        self.assertEqual(self.run_cli('done', 'FIRST', '--evidence', 'docs/agent/evidence/result.md').returncode, 0)
        self.assertEqual(self.current()[0]['status'], 'done')
        self.assertEqual(json.loads(self.run_cli('next').stdout)['id'], 'SECOND')

    def test_no_waivers_for_any_task(self):
        self.assertEqual(self.run_cli('start', 'FIRST').returncode, 0)
        for kind in ['implementation', 'environment', 'release_gate', 'release']:
            rows = self.current()
            rows[0]['kind'] = kind
            (self.docs / 'tasks.json').write_text(json.dumps(dict(schema_version=2, tasks=rows)))
            self.assertNotEqual(self.run_cli('waive', 'FIRST', '--reason', 'Self waiver',
                                            '--evidence', 'docs/agent/evidence/result.md').returncode, 0)
            self.assertEqual(self.current()[0]['status'], 'in_progress')

    def test_blocked_is_not_completion_and_independent_work_continues(self):
        self.rows[1]['depends_on'] = []
        self.save()
        self.assertEqual(self.run_cli('block', 'FIRST', '--reason', 'External test host unavailable').returncode, 0)
        self.assertEqual(json.loads(self.run_cli('next').stdout)['id'], 'SECOND')
        self.assertEqual(self.run_cli('block', 'SECOND', '--reason', 'External registry unavailable').returncode, 0)
        self.assertIn('BLOCKED', self.run_cli('next').stdout)
        self.assertNotEqual(self.run_cli('done', 'FIRST', '--evidence', 'docs/agent/evidence/result.md').returncode, 0)
        self.assertEqual(self.run_cli('resume', 'FIRST').returncode, 0)
        self.assertEqual(self.run_cli('start', 'FIRST').returncode, 0)

    def test_approval_is_not_inferred(self):
        self.rows[0]['approval_required'] = 'explicit external gate'
        self.save()
        self.assertNotEqual(self.run_cli('start', 'FIRST').returncode, 0)
        self.assertEqual(self.current()[0]['status'], 'todo')

    def test_symlink_or_empty_evidence_does_not_escape(self):
        (self.root / 'outside.md').write_text('not task evidence')
        (self.docs / 'evidence/link.md').symlink_to(self.root / 'outside.md')
        (self.docs / 'evidence/empty.md').touch()
        self.assertEqual(self.run_cli('start', 'FIRST').returncode, 0)
        for path in ['docs/agent/evidence/link.md', 'docs/agent/evidence/empty.md']:
            self.assertNotEqual(self.run_cli('done', 'FIRST', '--evidence', path).returncode, 0)


if __name__ == '__main__':
    unittest.main()
