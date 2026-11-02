export class AcceptanceBlocked extends Error {
  constructor(category, message) { super(message); this.category = category; }
}

export function requireAcceptedTerms(status) {
  if (status === 428) {
    throw new AcceptanceBlocked('environment', 'Receipt is unavailable, invalid, or for another user/terms version. Read and explicitly accept the current terms in the console first.');
  }
  if (status !== 200) throw new Error(`Consent validation returned unexpected status ${status}`);
}
