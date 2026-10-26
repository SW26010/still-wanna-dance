import { DAY } from '../../scripts/initial-priority.mjs';

// Include the first and last complete window; round intermediate starts to
// calendar-day boundaries, so adjacent gaps differ by at most one day.
export function sampleWindowStarts(trainingEnd, observationEnd, horizonDays, count = 5) {
  if (![trainingEnd, observationEnd, horizonDays, count].every(Number.isSafeInteger) || horizonDays <= 0 || count < 2) {
    throw Error('Expected integer dates, a positive horizon and at least two samples');
  }
  const span = (observationEnd - trainingEnd) / DAY - horizonDays;
  if (!Number.isInteger(span)) throw Error('Training and observation boundaries must share a calendar-day boundary');
  if (span < 0) return [];
  return [...new Set(Array.from({ length: count }, (_, i) => trainingEnd + Math.round(span * i / (count - 1)) * DAY))];
}
