import unittest
from analyze_discovery import classify


class DiscoveryAnalysisTest(unittest.TestCase):
    def test_positive_expiry_is_not_zero_ttl(self):
        e = {"Host": "play.udon.dance", "IPs": [], "After": {
            "At": "2026-10-21T12:00:02+08:00", "Observed": "2026-10-21T12:00:00+08:00",
            "CacheUntil": "2026-10-21T12:00:01+08:00", "CacheIPs": ["104.26.4.28"],
            "Leases": {"104.26.4.28": "2026-10-21T12:00:01+08:00"}, "LookupPending": False}}
        lookups = [{"ID": 1, "Host": "play.udon.dance", "Started": "2026-10-21T12:00:00+08:00", "TTLMS": 1000, "Error": ""}]
        self.assertEqual(classify(e, lookups)["classification"], "positive_ttl_expired")
        lookups[0]["TTLMS"] = 0
        self.assertEqual(classify(e, lookups)["classification"], "zero_ttl")
        e["After"]["Leases"]["104.26.4.28"] = "2026-10-21T12:01:00+08:00"
        self.assertEqual(classify(e, lookups)["classification"], "snapshot_race_or_other")


if __name__ == "__main__":
    unittest.main()
