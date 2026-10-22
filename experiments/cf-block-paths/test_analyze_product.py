import copy
import unittest

from analyze_product import validate


def fixture():
    request = {"Stage": "task-1344", "Host": "play.udon.dance", "Bytes": 17,
               "Length": 17, "Pinned": "104.16.0.1", "Remote": "104.16.0.1", "TLSVerified": True,
               "Status": 206, "Error": "", "Hash": "a", "ContentRange": "bytes 0-16/17"}
    task = {"Song": 1344, "OK": True, "Error": "", "Source": "MISS",
            "Files": [{"MD5": "a", "Size": 17, "Valid": True}]}
    return {"Schema": 1, "Method": "actual-prefetch-cf-history-two-v1", "Outcome": "failed", "BodyBytes": 17,
            "Sessions": [{"Round": 1, "Arm": "ordinary", "SetupBytes": 0, "Probes": [],
                          "Tasks": [task], "Requests": [request]}]}


class ProductAnalysisTests(unittest.TestCase):
    def test_partial_window_can_preserve_one_valid_task(self):
        validate(fixture())

    def test_success_cannot_hide_missing_body_or_wrong_peer(self):
        for change in ({"Bytes": 16}, {"Remote": "104.24.0.1"}, {"TLSVerified": False}, {"ContentRange": "bytes 1-17/18"}):
            data = fixture()
            data["Sessions"][0]["Requests"][0].update(change)
            with self.assertRaises(AssertionError):
                validate(data)

    def test_failed_attempt_is_preserved_before_valid_retry(self):
        data = fixture()
        failed = copy.deepcopy(data["Sessions"][0]["Requests"][0])
        failed.update(Status=0, Bytes=0, Length=0, Remote="", TLSVerified=False, Error="transport")
        data["Sessions"][0]["Requests"].insert(0, failed)
        validate(data)
        data["BodyBytes"] += 1
        with self.assertRaises(AssertionError):
            validate(data)

    def test_monitor_partial_body_is_cost_not_failed_publication(self):
        data = fixture()
        partial = copy.deepcopy(data["Sessions"][0]["Requests"][0])
        partial.update(Stage="setup", Bytes=3, Length=16)
        data["Sessions"][0]["Requests"].insert(0, partial)
        data["Sessions"][0]["SetupBytes"] = 3
        data["BodyBytes"] = 20
        validate(data)

    def test_v2_requires_hostname_verified_peer(self):
        data = fixture()
        data["Method"] = "actual-prefetch-cf-history-two-v2"
        data["Sessions"][0]["Requests"][0]["PeerVerified"] = True
        validate(data)
        data["Sessions"][0]["Requests"][0]["PeerVerified"] = False
        with self.assertRaises(AssertionError):
            validate(data)


if __name__ == "__main__":
    unittest.main()
