import { useEffect, useState } from 'react';
import { applyBlockEvent, type BlockDetail, type BlockEvent, type TimelineData } from './block-timeline-model';

export function useDocumentVisible() {
  const [visible, setVisible] = useState(!document.hidden);
  useEffect(() => {
    const change = () => setVisible(!document.hidden);
    document.addEventListener('visibilitychange', change);
    return () => document.removeEventListener('visibilitychange', change);
  }, []);
  return visible;
}

export function useBlockStream(enabled: boolean, revision: number) {
  const [data, setData] = useState<TimelineData>();
  const [speed, setSpeed] = useState(0);
  const [connection, setConnection] = useState<'connecting' | 'live' | 'offline' | 'error'>('connecting');
  useEffect(() => {
    // A resumed view must wait for this subscription's fresh snapshot before
    // restoring a pinned detail; the previous connection is no longer live.
    setConnection('connecting');
    if (!enabled) return;
    setSpeed(0);
    let disposed = false;
    const source = new EventSource('/api/block-demands/events');
    source.addEventListener('blocks', event => {
      if (disposed) return;
      try {
        const value: BlockEvent = JSON.parse((event as MessageEvent<string>).data);
        if (value.reset || !value.running) setSpeed(0);
        setData(old => applyBlockEvent(old, value));
        setConnection(value.running ? 'live' : 'offline');
      } catch { setConnection('error'); source.close(); }
    });
    source.addEventListener('download-speed', event => {
      if (disposed) return;
      try { setSpeed(JSON.parse((event as MessageEvent<string>).data).bytesPerSecond); }
      catch { setConnection('error'); source.close(); }
    });
    source.addEventListener('closed', () => {
      if (!disposed) { setConnection('offline'); setSpeed(0); }
      source.close();
    });
    source.onerror = () => { if (!disposed) setConnection('error'); };
    return () => { disposed = true; source.close(); };
  }, [enabled, revision]);
  return { data, connection, speed };
}

// Only the selected block has a detail stream. Pointer motion over unrelated
// empty pixels never starts requests. EventSource reconnects to a fresh snapshot.
export function useBlockDetail(id: string | undefined, enabled: boolean) {
  const [detail, setDetail] = useState<BlockDetail | null>();
  const [error, setError] = useState(false);
  useEffect(() => {
    setDetail(undefined); setError(false);
    if (!enabled || !id) return;
    let disposed = false;
    const source = new EventSource('/api/block-demands/events?block=' + encodeURIComponent(id));
    source.addEventListener('block-detail', event => {
      if (disposed) return;
      try { setDetail(JSON.parse((event as MessageEvent<string>).data)); setError(false); }
      catch { setError(true); source.close(); }
    });
    source.addEventListener('closed', () => { if (!disposed) setError(true); source.close(); });
    source.addEventListener('blocks', () => { if (!disposed) setDetail(null); });
    source.onerror = () => { if (!disposed) setError(true); };
    return () => { disposed = true; source.close(); };
  }, [id, enabled]);
  return { detail, error };
}
