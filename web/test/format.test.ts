import { describe, expect, it } from 'vitest';
import { cap, date, dateTime, duration, money, number, plural, relative, shortHash, shortId, time } from '../src/lib/format.ts';

const now = new Date('2026-10-08T14:05:00Z');
const ago = (s: number) => new Date(now.getTime() - s * 1000).toISOString();

describe('format', () => {
  it.each([
    [number(1204), '1,204'],
    [number(0), '0'],
    [plural(1, 'deed'), '1 deed'],
    [plural(1204, 'deed'), '1,204 deeds'],
    [money(12000, 'usd'), '$120.00'],
    [money(51025, 'USD'), '$510.25'],
    [money(500, 'jpy'), '¥500'],
    [time('2026-10-08T08:35:00Z', 'Asia/Kolkata'), '14:05'],
    [time('2026-10-08T14:05:00Z'), '14:05'],
    [date('2026-10-02T23:30:00Z', 'Asia/Kolkata'), '3 Oct 2026'],
    [date('2026-10-02T12:00:00Z'), '2 Oct 2026'],
    [dateTime('2026-10-02T12:00:09Z'), '2 Oct 2026, 12:00:09 UTC'],
    [relative(ago(3), now), 'just now'],
    [relative(ago(45), now), '45s ago'],
    [relative(ago(130), now), '2m ago'],
    [relative(ago(3 * 3600 + 5), now), '3h ago'],
    [relative(ago(5 * 86400), now), '5d ago'],
    [relative(ago(9 * 86400), now), '29 Sept 2026'],
    [shortId('agt_01k6h7kp6msmzrhdzwtcr3vqvp'), 'agt_01k6…vqvp'],
    [shortHash('sha256:3f9a' + '0'.repeat(56) + 'c21e'), 'sha256:3f9a…c21e'],
    [duration(41), '41 ms'],
    [duration(1500), '1.5 s'],
    [cap(7), '7'],
    [cap(120), '99+'],
  ])('%s', (got, want) => expect(got).toBe(want));
});
