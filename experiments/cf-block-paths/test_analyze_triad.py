import json
import tempfile
import unittest
import zipfile
from pathlib import Path

from analyze_triad import (analyze, holdout, predictions, protocol_pairs, ranks,
                           public_sampling_period, rate, recommendation, spearman, success, wilson)
from export_triad import archive_member, public_protocol, sanitize, sanitize_freeze
from verify_triad import verify_response


def row(ip='104.16.0.1', method='https', error='', ms=1000, stage='measure'):
    return dict(Stage=stage, Pool='A', IP=ip, Method=method, Song=1343, Round=1, Position=0,
                Started='2000-01-01T08:00:00Z', Requested=2097152,
                Bytes=2097152 if not error else 100, Status=200 if method=='speed' else 206,
                Error=error, ElapsedMS=ms, ContentLength=2097152, Protocol='HTTP/1.1',
                Reused=False, Remote=ip, ContentEncoding='', Hash='a'*64,
                TLSVerified=method!='http', SNI='host', Host='host',
                FirstByteMS=100, BodyMS=ms-100, Cache='HIT', Ray='id-HKG')


class TriadTests(unittest.TestCase):
    def test_failures_never_get_complete_rate(self):
        for err in ('body_read', 'transport', 'redirect', 'headers', 'screened_out'):
            self.assertEqual(rate(row(error=err)), 0)
        r=row();r['Bytes']-=1
        self.assertFalse(success(r))
        self.assertEqual(rate(None),0)

    def test_ties_and_undefined_correlation(self):
        self.assertEqual(ranks([3,1,1]),[3,1.5,1.5])
        self.assertAlmostEqual(spearman([1,2,3],[3,2,1]),-1)
        self.assertIsNone(spearman([0,0,0],[1,2,3]))
        self.assertIsNone(spearman([1,2],[2,1]))

    def test_failures_are_in_correlation_not_dropped(self):
        s=[row(str(i),'speed',ms=(i+1)*1000) for i in range(3)]
        v=[row(str(i),error='body_read' if i==0 else '',ms=(4-i)*1000) for i in range(3)]
        out=recommendation(s,v)
        self.assertEqual(out['measuredPairs'],3)
        self.assertEqual(out['bothComplete'],2)
        self.assertIsNotNone(out['spearmanFailuresTiedLast'])
        self.assertIsNone(out['spearmanCompleteOnly'])
        self.assertEqual(out['speedSuccessVideoFailure'],1)
        self.assertEqual(out['selectedTop1RatioToActualBest'],0)

    def test_screened_out_is_unknown_not_measured_failure(self):
        out=recommendation([row('x','speed',error='screened_out')], [row('x',error='screened_out')])
        self.assertEqual(out['measuredPairs'],0)
        pair=protocol_pairs([row('x','http',error='screened_out'),row('x',error='screened_out')])
        self.assertEqual(pair['counts'],{'screenedOut':1})

    def test_pair_never_crosses_resource_or_round(self):
        a,b=row(method='http'),row()
        b['Round']=2
        self.assertEqual(protocol_pairs([a,b])['counts'],{'missingPair':2})
        b['Round']=1;b['Song']=1344
        self.assertEqual(protocol_pairs([a,b])['counts'],{'missingPair':2})

    def test_pair_direction_and_partial_outcomes(self):
        result=protocol_pairs([row('a','http',ms=1000),row('a',ms=2000),row('b','http'),row('b',error='body_read')])
        self.assertEqual(result['httpRateOverHttpsRate']['median'],2)
        self.assertEqual(result['counts'],{'bothComplete':1,'httpOnly':1})

    def test_holdout_uses_frozen_pick(self):
        first={'Window':1,'Rows':[row('old','speed',ms=100),row('new','speed',ms=1000)]}
        later={'Window':2,'Rows':[row('old',ms=4000),row('new',ms=1000)]}
        frozen=predictions(first)
        result=holdout(frozen,later)['A']['https']['speed']['topK']
        self.assertEqual(result['1']['ips'],['old'])
        self.assertEqual(result['1']['bestSelectedObservedRatio'],.25)
        self.assertEqual(result['3']['bestSelectedObservedRatio'],1)

    def test_missing_holdout_pick_not_replaced(self):
        frozen={'window':1,'picks':{'A':{'speed':['missing']}}}
        result=holdout(frozen,{'Window':2,'Rows':[row('new')]})['A']['https']['speed']['topK']['1']
        self.assertEqual(result['unknown'],1)
        self.assertEqual(result['successful'],0)

    def test_wilson_not_zero_width_at_extremes(self):
        self.assertGreater(wilson(0,104)[1],0)
        self.assertLess(wilson(104,104)[0],1)

    def test_missing_prediction_not_reported_as_failure(self):
        out=recommendation([row('missing','speed')],[row('actual')])
        self.assertEqual(out['speedSuccessVideoFailure'],0)
        self.assertEqual(out['speedSuccessVideoUnknown'],1)
        self.assertIsNone(out['selectedTop1RatioToActualBest'])

    def test_public_trace_removes_private_fields(self):
        r=row();r.update(Local='private-source',ErrorDetail='signed-url',Location='private-location',ETag='private-etag')
        data={'Started':'2000-01-01T08:00:00Z','Finished':'2000-01-01T08:01:00Z','Rows':[r]}
        exported=sanitize(data)['Rows'][0]
        for key in ('Local','ErrorDetail','Location','ETag','Ray','Started'):
            self.assertNotIn(key,exported)
        self.assertEqual(exported['StartMS'],0)
        self.assertEqual(exported['Colo'],'HKG')
        self.assertEqual(len(exported['ETagSHA256']),64)

    def test_public_trace_redacts_prepare_url(self):
        from analyze_triad import public_error
        private='prepare_1343_Get "https://private.example/video?token=secret": timeout'
        self.assertEqual(public_error(private),'prepare_1343')
        data={'Started':'2000-01-01T08:00:00Z','Rows':[],'Error':private}
        self.assertEqual(sanitize(data)['Error'],'prepare_1343')
        self.assertEqual(public_error('critical_content_mismatch'),'critical_content_mismatch')

    def test_summary_keeps_duration_without_absolute_times(self):
        data=dict(Schema=1,Method='cf-triad-v1',Window=1,Rows=[],Candidates=[],
                  BodyBytes=0,Outcome='complete',Error='',CandidatesSHA256='a'*64,
                  Started='2000-01-01T08:00:00.123456Z',Finished='2000-01-01T08:01:00.123456Z')
        result=analyze(data,{'prefixes':[]})
        self.assertEqual(result['samplingDate'],'2000-01-01')
        self.assertEqual(result['samplingPeriod'],'afternoon')
        self.assertEqual(result['wallSeconds'],60)
        self.assertNotIn('started',result)
        self.assertNotIn('finished',result)
        self.assertNotIn('T08:',json.dumps(result))

    def test_sampling_period_uses_shanghai_date(self):
        result=public_sampling_period('2000-01-01T21:00:00Z')
        self.assertEqual(result,{'samplingDate':'2000-01-02','samplingPeriod':'night','samplingTimezone':'Asia/Shanghai'})

    def test_freeze_exports_only_relative_ordering(self):
        private=dict(frozenFileModifiedUTC='2000-01-01T08:01:10Z',
                     w1Finished='2000-01-01T16:01:00+08:00',w2Started='2000-01-01T16:02:00+08:00',
                     w1RawSHA256='a'*64,w1SelectionMatchesRecordedRanks=True,frozenBeforeW2=True,
                     windowStartSeparationSeconds=120,frozenBuildsMatch=True,files={},
                     w2ReferenceMatchesHistorical={},scope='freeze check',privateNote='private')
        result=sanitize_freeze(private)
        self.assertEqual(result['freezeAfterW1FinishedSeconds'],10)
        self.assertEqual(result['w2StartedAfterFreezeSeconds'],50)
        for key in ('frozenFileModifiedUTC','w1Finished','w2Started','privateNote'):
            self.assertNotIn(key,result)
        self.assertTrue(result['frozenBeforeW2'])

    def test_public_protocol_keeps_method_without_internal_attribution(self):
        text='本协议在内部任务的新采样前冻结，方法 `cf-triad-v1`。\n\n相同 Range，证书校验。\n'
        self.assertEqual(public_protocol(text),'本协议在采样前冻结，方法 `cf-triad-v1`。\n\n相同 Range，证书校验。\n')

    def test_zip_metadata_does_not_copy_local_modification_time(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'sample.json';p.write_bytes(b'{}\n')
            archive=Path(tmp)/'sample.zip'
            with zipfile.ZipFile(archive,'w') as z:
                z.writestr(*archive_member(p,p.name))
            with zipfile.ZipFile(archive) as z:
                self.assertEqual(z.getinfo(p.name).date_time,(1980,1,1,0,0,0))
                self.assertEqual(z.read(p.name),b'{}\n')

    def test_independent_check_rejects_wrong_range(self):
        r=row();r.update(Range='bytes=0-2097151',ContentRange='bytes 1-2097152/9999999',ETag='tag',HeadersMS=101,Progress=[])
        with self.assertRaises(AssertionError):
            verify_response(r,{'Host':'host','Size':9999999,'ETag':'tag'})

    def test_independent_check_rejects_overlapping_progress(self):
        r=row();r.update(Range='bytes=0-2097151',ContentRange='bytes 0-2097151/9999999',ETag='tag',HeadersMS=100,Progress=[{'Bytes':524288,'MS':200},{'Bytes':262144,'MS':300}])
        with self.assertRaises(AssertionError):
            verify_response(r,{'Host':'host','Size':9999999,'ETag':'tag'})


if __name__=='__main__':
    unittest.main()
