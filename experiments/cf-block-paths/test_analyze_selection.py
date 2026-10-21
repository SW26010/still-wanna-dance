import json
import tempfile
import unittest
from pathlib import Path

from analyze_selection import cost_replay, paired, summarize, validate_measurements


def row(arm, song=1, round=1, ms=100, ok=True):
    return dict(Song=song, Arm=arm, IP=arm, Round=round, OK=ok,
                Error="" if ok else "request_failed", ElapsedMS=ms, Bytes=100,
                Prefix8MS=ms/2, FirstBlockMS=1, CPUMS=1, PeakActive=1, Requests=[])


class SelectionAnalysisTest(unittest.TestCase):
    def test_same_address_noise_never_amortizes_search(self):
        a, b = row("dns-short", ms=120), row("union-short", ms=100)
        b["IP"] = a["IP"]
        replay = cost_replay(paired([a,b], "union-short"), 1000, 200)
        self.assertEqual(replay["sameIPControlPairs"], 1)
        self.assertIsNone(replay["medianHoldoutSavingPerTaskMS"])
        self.assertIsNone(replay["tasksToAmortizeExtraSearch"])
        b["IP"] = "another-address"
        replay = cost_replay(paired([a,b], "union-short"), 1000, 200)
        self.assertEqual(replay["tasksToAmortizeExtraSearch"], 40)

    def test_content_and_accounting_are_not_trusted_from_ok_flag(self):
        a=row("reference");a.update(Size=100,Bytes=100,Requests=[dict(Bytes=100,Length=100,Offset=0,Hash="expected",Status=206,Remote="reference",Error="")])
        b=row("training");b.update(Size=100,Bytes=100,Requests=[dict(Bytes=100,Length=100,Offset=0,Hash="wrong",Status=206,Remote="training",Error="")])
        d=dict(Training=[a,b],Evaluation=[],Probes=[],BodyBytes=200,Outcome="failed",Selected={})
        with self.assertRaisesRegex(AssertionError,"content mismatch"):
            validate_measurements(d)
        b["Requests"][0]["Hash"]="expected"
        validate_measurements(d)
        d["BodyBytes"]=199
        with self.assertRaisesRegex(AssertionError,"total body accounting"):
            validate_measurements(d)

    def test_pairs_use_equal_song_round_and_keep_failures(self):
        rows = [row("dns-short", ms=120), row("union-short", ms=100),
                row("dns-short", round=2), row("union-short", round=2, ok=False),
                row("dns-short", song=2), row("union-short", round=0, ms=1)]
        result = paired(rows, "union-short")
        self.assertEqual(result["cleanPairs"], 1)
        self.assertAlmostEqual(result["medianThroughputGain"], .2)
        self.assertEqual(result["medianTimeSavedMS"], 20)
        self.assertEqual(len(result["pairs"]), 3)
        self.assertEqual(result["pairs"][-1]["candidateError"], "not_executed")

    def test_interrupted_window_and_unknown_method(self):
        d = dict(Schema=1, Method="equal-membership-cold-probes-v1", Candidates=[],
                 Probes=[], Training=[], Evaluation=[], Started="2026-10-22T00:00:00Z",
                 Finished="2026-10-22T00:01:00Z", Outcome="failed", Error="preflight",
                 Selected={}, BodyBytes=0, PreparationMS=0, ScreenMS=0, ReferenceMS=0)
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)/"selection.json"
            p.write_text(json.dumps(d), encoding="utf-8")
            result = summarize(p)
            self.assertEqual(result["missingEvaluationLoads"], 30)
            self.assertIsNone(result["costReplay"]["tasksToAmortizeFullSearch"])
            self.assertFalse(any(r["retains90Percent"] for r in result["screenRetention"]))
            d["Method"] = "other"
            p.write_text(json.dumps(d), encoding="utf-8")
            with self.assertRaisesRegex(ValueError,"unsupported"):
                summarize(p)


if __name__ == "__main__":
    unittest.main()
