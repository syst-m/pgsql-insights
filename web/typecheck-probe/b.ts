/* The loud half: a key the *type* requires and the *value* lacks is TS2741.
 * This file exists to be the one error `npx tsc -p typecheck-probe` reports;
 * deleting it makes that command exit 0. */
export const y: { a: number; b: number } = { a: 1 };
