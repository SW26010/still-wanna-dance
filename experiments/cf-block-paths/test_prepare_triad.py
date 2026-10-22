import ipaddress
import tempfile
import unittest
from pathlib import Path

from prepare_triad import prepare


class PrepareTriadTests(unittest.TestCase):
    def test_factual_extraction_and_reproducible_equal_controls(self):
        a = [f'172.64.{n}.1' for n in range(128,256,16)]
        a += [f'104.{s}.{n}.1' for s in range(16,28) for n in range(0,256,32)]
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp)/'source.txt'
            p.write_text('CandidateIps = new string[104] { '+','.join('"'+ip+'"' for ip in a)+' };',encoding='utf-8')
            x,y = prepare(p),prepare(p)
        self.assertEqual(x,y)
        self.assertEqual(x['pools']['A'],a)
        self.assertEqual(x['addressSpace'],1524736)
        nets = [ipaddress.ip_network(p) for p in x['prefixes']]
        for ips in x['pools'].values():
            self.assertEqual(len(ips),104)
            self.assertEqual(len(set(ips)),104)
            self.assertTrue(all(any(ipaddress.ip_address(ip) in n for n in nets) for ip in ips))
        counts=x['structure']['B_stratified']['prefixCounts']
        self.assertEqual(sorted(counts.values()),[6]+[7]*14)


if __name__=='__main__':
    unittest.main()
