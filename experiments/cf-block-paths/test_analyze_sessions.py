import copy
import unittest

from analyze_sessions import ordinary_paths, pair_sessions, session_cost


def session(arm="ordinary", setup=0):
    return {"Round": 1, "Arm": arm, "Error": "", "SetupError": "", "SetupMS": setup,
            "SetupBytes": 0, "Requests": [], "Probes": [],
            "Started": "2026-01-01T00:00:00+08:00", "Finished": "2026-01-01T00:00:02+08:00",
            "Tasks": [{"Song": song, "OK": True, "ElapsedMS": 100, "SessionElapsedMS": setup+(i+1)*150,
                       "Started": "2026-01-01T00:00:00+08:00"} for i, song in enumerate((1344, 10552, 10553))]}


class SessionAuditTests(unittest.TestCase):
    def test_wall_is_observed_and_includes_overhead_not_sum(self):
        base, candidate = session(), session("monitor", 1000)
        candidate["Finished"] = "2026-01-01T00:00:03.5+08:00"
        pair = pair_sessions([base, candidate])[0]
        self.assertEqual(pair["fullSessionSavedMS"], -1500)
        self.assertEqual(pair["businessCompletionSavedMS"], -1000)
        self.assertEqual(pair["sumSetupAndTaskSavedMS"], -1000)
        self.assertEqual(session_cost(base)["fullSessionWallMS"], 2000)
        self.assertEqual(pair["prefixes"][-1]["observedWallSavedMS"], -1000)

    def test_missing_or_failed_session_does_not_create_gain(self):
        for change in ({"Error": "failed"}, {"SetupError": "api_523"}, {"Tasks": []}):
            bad = session("monitor")
            bad.update(change)
            pairs = pair_sessions([session(), bad])
            self.assertFalse(pairs[0]["comparable"])
            self.assertNotIn("fullSessionSavedMS", pairs[0])
            self.assertEqual(pairs[-1]["candidateState"], "not_executed")

    def test_pairing_does_not_borrow_other_round_or_song(self):
        candidate = session("monitor")
        candidate["Round"] = 2
        self.assertFalse(any(p["comparable"] for p in pair_sessions([session(), candidate])))
        candidate["Round"] = 1
        candidate["Tasks"].reverse()
        with self.assertRaises(AssertionError):
            pair_sessions([session(), candidate])

    def test_overlapping_requests_use_envelope_and_v1_has_no_peer(self):
        s = session()
        q = {"Stage": "task-1344", "Host": "play.udon.dance", "Status": 206, "Error": "",
             "StartMS": 5, "HeadersMS": 10, "EndMS": 50, "PeerVerified": True, "TLSVerified": True,
             "Remote": "2606:4700:20::681a:41c", "Bytes": 10, "Reused": False, "Cache": "HIT"}
        second = copy.deepcopy(q)
        second.update(StartMS=20, HeadersMS=25, EndMS=70, Reused=True)
        s["Requests"] = [q, second]
        d = {"Method": "actual-prefetch-cf-history-two-v2", "Sessions": [s]}
        result = ordinary_paths(d)[0]
        self.assertEqual(result["mediaEnvelope"]["wallSpanMS"], 65)
        self.assertEqual(result["peers"][0]["family"], 6)
        self.assertEqual(result["peers"][0]["reusedResponses"], 1)
        self.assertEqual(result["afterLastRecordedRequestMS"], 30)
        d["Method"] = "actual-prefetch-cf-history-two-v1"
        result = ordinary_paths(d)[0]
        self.assertEqual(result["peers"], [])
        self.assertEqual(result["unattributedMediaResponses"], 2)
        d["Method"] = "actual-prefetch-cf-history-two-v2"
        s["Requests"][0]["TLSVerified"] = False
        result = ordinary_paths(d)[0]
        self.assertEqual(result["peers"][0]["responses"], 1)
        self.assertEqual(result["unattributedMediaResponses"], 1)


if __name__ == "__main__":
    unittest.main()
