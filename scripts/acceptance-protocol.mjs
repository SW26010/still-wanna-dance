// Keep the acceptance contract aligned with cacheproxy's redirect/video parser.
export class AcceptanceBlocked extends Error {
  constructor(category, message) { super(message); this.category = category; }
}

export function videoRedirect(status, location) {
  const changed = message => { throw new AcceptanceBlocked('upstream-protocol-change', message); };
  if (![301, 302, 307, 308].includes(status)) changed(`Unexpected playback API status: ${status}`);
  let url;
  try { url = new URL(location); } catch { changed('Missing or invalid absolute Location'); }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.hash ||
      !['play.udon.dance', 'nya.xin.moe'].includes(url.hostname) ||
      !/^\/files\/[0-9]+\/[1-9][0-9]*-[a-zA-Z0-9]+\.mp4$/.test(url.pathname)) {
    changed(`Unsupported video Location: ${location}`);
  }
  // URLSearchParams tolerates malformed escapes/semicolons that Go rejects.
  try {
    if (url.search.includes(';')) changed('Unsupported query separator');
    decodeURIComponent(url.search);
  } catch { changed('Malformed video query'); }
  const e = url.searchParams.getAll('e'), s = url.searchParams.getAll('s');
  if (e.length !== 1 || !/^[a-fA-F0-9]{32}$/.test(e[0]) || s.length !== 1 ||
      !/^\+?[0-9]+$/.test(s[0]) || Number(s[0]) < 1 || Number(s[0]) > 2 ** 31) {
    changed(`Unsupported video checksum/size: ${location}`);
  }
  return url;
}
