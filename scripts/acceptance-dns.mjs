import { Resolver } from 'node:dns/promises';
import net from 'node:net';
import os from 'node:os';

// resolve4 queries DNS directly instead of consulting the OS hosts file.
// Bound DNS time independently; never fall back to dns.lookup on failure.
const resolver = new Resolver({ timeout: 3000, tries: 2 });

export function createUpstreamLookup(resolve4 = host => resolver.resolve4(host), interfaces = os.networkInterfaces) {
  return (hostname, options, callback) => {
    Promise.resolve().then(() => resolve4(hostname)).then(addresses => {
      const local = new Set(Object.values(interfaces()).flat().map(entry => entry.address));
      const usable = addresses.filter(address => {
        if (!net.isIPv4(address) || local.has(address)) return false;
        const [a, b] = address.split('.').map(Number);
        return a !== 0 && a !== 10 && a !== 127 && a < 224 &&
          !(a === 169 && b === 254) && !(a === 172 && b >= 16 && b <= 31) &&
          !(a === 192 && b === 168) && !(a === 100 && b >= 64 && b <= 127);
      });
      if (!usable.length) throw new Error(`Independent DNS returned no non-local IPv4 address for ${hostname}`);
      return usable.map(address => ({ address, family: 4 }));
    }).then(addresses => {
      if (options?.all) callback(null, addresses);
      else callback(null, addresses[0].address, 4);
    }, callback);
  };
}

export const upstreamLookup = createUpstreamLookup();
