import unittest

from analyze_sweep import peak_occupancy


class OccupancyTest(unittest.TestCase):
    def test_adjacent_requests_do_not_overlap_due_to_float_addition(self):
        self.assertGreater(0.1 + 0.2, 0.3)
        self.assertEqual(peak_occupancy([
            {"startMS": 0.1, "elapsedMS": 0.2},
            {"startMS": 0.3, "elapsedMS": 0.2},
        ]), 1)

    def test_real_one_nanosecond_overlap_is_preserved(self):
        self.assertEqual(peak_occupancy([
            {"startMS": 0.1, "elapsedMS": 0.200001},
            {"startMS": 0.3, "elapsedMS": 0.2},
        ]), 2)


if __name__ == "__main__":
    unittest.main()
