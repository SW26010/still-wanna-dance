import unittest
import json
import tempfile
from pathlib import Path
from analyze_family import pairs_for, aggregate_family, family_window


class FamilyAnalysisTest(unittest.TestCase):
    def test_same_ip_is_retained_as_control(self):
        runs = [{"round": 1, "song": 1, "arm": a, "ip": "same", "ok": True,
                 "error": "", "elapsedMS": ms, "prefix8MS": 50}
                for a, ms in [("auto", 100), ("v4", 80)]]
        pairs, failed = pairs_for(runs, "auto", "v4")
        self.assertFalse(failed)
        self.assertTrue(pairs[0]["sameIP"])
        self.assertEqual(pairs[0]["throughputGainPercent"], 25)

    def test_failure_is_not_a_zero_speed_or_silent_exclusion(self):
        runs = [{"round": 1, "song": 1, "arm": a, "ip": a, "ok": a == "auto",
                 "error": "" if a == "auto" else "timeout", "elapsedMS": 100, "prefix8MS": 50}
                for a in ("auto", "extra")]
        pairs, failed = pairs_for(runs, "auto", "extra")
        self.assertFalse(pairs)
        self.assertEqual(failed[0]["arms"]["extra"], "timeout")

    def test_interrupted_training_preserves_samples_before_selection(self):
        raw = {"Schema": 2, "Started": "start", "Finished": "end", "Outcome": "failed",
               "Error": "training_context", "DNS": [], "Selected": {}, "Plan": [
                   {"Round": 0, "Song": 1, "Arm": "auto"}], "Runs": []}
        runs = [{"Round": n, "Song": 1343, "Arm": "v6", "IP": "::1", "Size": 1,
                 "Bytes": 1, "OK": True, "Error": "", "ElapsedMS": 100+n, "PeakActive": 1,
                 "Requests": [{"Bytes": 1, "Length": 1, "Offset": 0, "Hash": "same",
                               "Error": "", "Status": 206, "Remote": "::1"}]}
                for n in (1, 2, 3)]
        training = {"Candidates": [{"IP": "::1", "Arm": "v6", "DurationsMS": [101, 102, 103],
                                     "Failures": 0, "MedianMS": 0}], "Runs": runs, "Winners": {}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "window.json"
            path.write_text(json.dumps(raw), encoding="utf-8")
            (path.parent / "training.json").write_text(json.dumps(training), encoding="utf-8")
            result = family_window(path)
        self.assertEqual(len(result["blockTraining"]["runs"]), 3)
        self.assertEqual(result["missingRuns"], 1)
        self.assertEqual(aggregate_family([result])["completeWindows"], 0)


if __name__ == "__main__":
    unittest.main()
