import test from 'node:test';
import assert from 'node:assert/strict';
import { AcceptanceBlocked, videoRedirect } from './acceptance-protocol.mjs';

const video = 'http://play.udon.dance/files/2403/1343-version.mp4?e=28711962048bed664c98f27e1d9d5842&s=32867177';

test('accept every supported redirect over HTTP and HTTPS, without fixing a node to a host', () => {
  for (const status of [301, 302, 307, 308]) {
    for (const scheme of ['http:', 'https:']) {
      for (const host of ['play.udon.dance', 'nya.xin.moe', 'media.future.example']) {
        const url = new URL(video); url.protocol = scheme; url.hostname = host;
        assert.equal(videoRedirect(status, url.href).href, url.href);
      }
    }
  }
});

test('unexpected API responses are protocol blockers, not program assertion failures', () => {
  for (const [status, location] of [
    [200, video], [303, video], [302, undefined], [302, '/video.mp4'],
    [302, video.replace('play.udon.dance', 'localhost')],
    [302, video.replace('http:', 'ftp:')], [302, video + '#fragment'],
    [302, video.replace('http://', 'http://user:password@')],
    [302, video.replace('1343-version', '%31343-version')],
    [302, video.replace('28711962048bed664c98f27e1d9d5842', 'invalid')],
    [302, video + '&e=28711962048bed664c98f27e1d9d5842'],
    [302, video + '&s=1'], [302, video.replace('32867177', '0')],
    [302, video.replace('32867177', '2147483649')],
    [302, video + '&token=%zz'], [302, video + '&token=x;y'],
  ]) {
    assert.throws(() => videoRedirect(status, location), error =>
      error instanceof AcceptanceBlocked && error.category === 'upstream-protocol-change');
  }
});
