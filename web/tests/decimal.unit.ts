import { expect, it } from 'vitest';
import { stepDecimal } from '../packages/ui/src/lib/decimal';

it('steps exact decimal values beyond JS safe integer range', () => {
  expect(stepDecimal('9007199254740993.125', '0.001', 1)).toBe('9007199254740993.126');
  expect(stepDecimal('0.01', '0.02', -1)).toBe('-0.01');
  expect(stepDecimal('-1', '1', 1)).toBe('0');
  expect(stepDecimal('99.99', 'invalid', 1)).toBe('99.99');
  expect(stepDecimal('99.99', '.01', 1)).toBe('100');
  expect(stepDecimal('99.99', '0.01', 1)).toBe('100');
  expect(stepDecimal('10', '1', 1, '0', '10')).toBe('10');
});
