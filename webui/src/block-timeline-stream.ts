import { useEffect, useState } from 'react';
import { applyBlockEvent, type BlockDetail, type TimelineData } from './block-timeline-model';
import { subscribeRealtime } from './realtime-stream';

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
  const [cacheBytes, setCacheBytes] = useState<number>();
  const [connection, setConnection] = useState<'connecting' | 'live' | 'offline' | 'error'>('connecting');
  useEffect(() => {
    // A resumed view must wait for this subscription's fresh snapshot before
    // restoring a pinned detail; the previous connection is no longer live.
    setConnection('connecting');
    if (!enabled) return;
    setSpeed(0);
    return subscribeRealtime('/api/block-demands/events', {
      receive: (event, value) => {
        if (event === 'blocks') {
          if (!value || typeof value.running !== 'boolean' || !Array.isArray(value.blocks)) throw new Error('invalid blocks');
          if (value.reset || !value.running) setSpeed(0);
          if (!value.running) setCacheBytes(undefined);
          setData(old => applyBlockEvent(old, value));
          setConnection(value.running ? 'live' : 'offline');
        } else if (event === 'download-speed') {
          if (!Number.isFinite(value?.bytesPerSecond)) throw new Error('invalid speed');
          setSpeed(value.bytesPerSecond);
        } else if (event === 'block-cache') {
          if (!Number.isFinite(value?.bytes)) throw new Error('invalid cache size');
          setCacheBytes(value.bytes);
        } else if (event === 'closed') {
          setConnection('offline'); setSpeed(0); setCacheBytes(undefined); return false;
        } else throw new Error('invalid block event');
      },
      connection: state => {
        setConnection(state === 'connecting' || state === 'paused' ? 'connecting' : 'error');
        if (state === 'connecting' || state === 'paused') setSpeed(0);
      },
    });
  }, [enabled, revision]);
  return { data, connection, speed, cacheBytes };
}

// Only the selected block has a detail stream. Pointer motion over unrelated
// empty pixels never starts requests. Reconnection starts with a fresh snapshot.
export function useBlockDetail(id: string | undefined, enabled: boolean) {
  const [detail, setDetail] = useState<BlockDetail | null>();
  const [error, setError] = useState(false);
  useEffect(() => {
    setDetail(undefined); setError(false);
    if (!enabled || !id) return;
    return subscribeRealtime('/api/block-demands/events?block=' + encodeURIComponent(id), {
      receive: (event, value) => {
        if (event === 'block-detail') { setDetail(value); setError(false); }
        else if (event === 'blocks') setDetail(null);
        else if (event === 'closed') { setError(true); return false; }
        else throw new Error('invalid block detail event');
      },
      connection: state => {
        setError(state === 'disconnected' || state === 'invalid');
        if (state === 'connecting' || state === 'paused') setDetail(undefined);
      },
    });
  }, [id, enabled]);
  return { detail, error };
}
