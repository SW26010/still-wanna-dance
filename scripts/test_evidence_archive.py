import hashlib
import importlib.util
import io
import json
import tempfile
import unittest
import zipfile
from pathlib import Path

spec = importlib.util.spec_from_file_location('archive', Path(__file__).with_name('evidence_archive.py'))
archive = importlib.util.module_from_spec(spec)
spec.loader.exec_module(archive)

def fixture(name='docs/evidence/trial.json', content=b'{"result":"failure"}\n'):
    output = io.BytesIO()
    with zipfile.ZipFile(output, 'w') as z:
        z.writestr(name, content)
        z.writestr('_provenance/inventory.json', json.dumps({'files':[{'path':name,'bytes':len(content),'sha256':hashlib.sha256(content).hexdigest()}]}))
    raw = output.getvalue()
    return raw, {'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}

class EvidenceArchiveTests(unittest.TestCase):
    def test_corruption_and_traversal_rejected(self):
        raw, record = fixture()
        with self.assertRaisesRegex(ValueError, 'SHA256'):
            archive.verified_members(raw[:-1], record)
        raw, record = fixture('../outside')
        with self.assertRaisesRegex(ValueError, 'Unsafe'):
            archive.verified_members(raw, record)

    def test_restore_preserves_failure_and_refuses_overwrite(self):
        raw, record = fixture()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(archive.restore(raw, record, root), 2)
            self.assertEqual(archive.restore(raw, record, root), 2)
            target = root/'docs/evidence/trial.json'
            self.assertIn(b'failure', target.read_bytes())
            target.write_bytes(b'local work')
            with self.assertRaisesRegex(ValueError, 'differs'):
                archive.restore(raw, record, root)
            self.assertEqual(target.read_bytes(), b'local work')

if __name__=='__main__':unittest.main()
