import copy
import unittest
from evidence_time import relative_times


class EvidenceTimeTest(unittest.TestCase):
    def test_nested_times_share_one_baseline_without_changing_evidence(self):
        data = {"started": "2026-01-01T08:00:01.0000002+08:00",
                "observation": [{"At": "2026-01-01T00:00:00.0000001Z",
                                 "lease": "2026-01-01T00:00:01.0000002Z"}],
                "unset": "0001-01-01T00:00:00Z", "missing": None,
                "sha256": "a" * 64, "elapsedMS": 1000.0001, "bytes": 512,
                "outcome": "failed", "date": "2026-01-01"}
        original = copy.deepcopy(data)
        out = relative_times(data)
        self.assertEqual(data, original)
        self.assertEqual(out["started"], "T+1.0000001s")
        self.assertEqual(out["observation"][0], {"At": "T+0s", "lease": "T+1.0000001s"})
        self.assertIsNone(out["unset"])
        for k in ("missing", "sha256", "elapsedMS", "bytes", "outcome", "date"):
            self.assertEqual(out[k], data[k])
        self.assertEqual(relative_times(out), out)

    def test_baselines_are_per_file_and_unset_does_not_create_epoch(self):
        self.assertEqual(relative_times({"at": "2030-05-01T00:00:00Z"})["at"], "T+0s")
        self.assertEqual(relative_times({"at": "2020-05-01T00:00:00Z"})["at"], "T+0s")
        self.assertIsNone(relative_times({"at": "0001-01-01T00:00:00Z"})["at"])
        self.assertEqual(relative_times({"count": 3}), {"count": 3})


if __name__ == "__main__":
    unittest.main()
