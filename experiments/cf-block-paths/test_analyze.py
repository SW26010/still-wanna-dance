import unittest
from analyze import comparison, aggregate


class AnalysisTest(unittest.TestCase):
    def test_speed_and_time_are_not_same_percentage(self):
        r = comparison({"ElapsedMS": 1000, "Prefix8MS": 400}, {"ElapsedMS": 800, "Prefix8MS": 440})
        self.assertAlmostEqual(r["throughputGainPercent"], 25)
        self.assertAlmostEqual(r["timeSavedPercent"], 20)
        self.assertAlmostEqual(r["prefix8ChangePercent"], 10)

    def test_incomplete_window_does_not_count_as_cross_time_success(self):
        w = {"window": "failed", "outcome": "failed", "pairs": [{"song": 1, "throughputGainPercent": 30, "prefix8ChangePercent": -5}]}
        r = aggregate([w])
        self.assertEqual(r["completeWindowCount"], 0)
        self.assertEqual(r["successfulPairs"], 1)
        self.assertEqual(r["decision"], "pending_cross_time_evidence")


if __name__ == "__main__":
    unittest.main()
