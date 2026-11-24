/* The silent half of the probe: an annotation checks type -> value, never
 * value -> type. `f.json` has both `a` and `b`.
 *
 * `x`'s type omits `b`, which the value has: no diagnostic, because
 * excess-property checking applies only to fresh object literals and an
 * imported JSON module is a typed binding, not a fresh literal.
 *
 * `z`'s type names both keys: also no diagnostic.
 *
 * Together with b.ts this is the evidence for the R16.8 corollary that a
 * fixture annotated `Envelope<QueryView>` cannot fail on a key the wire gained
 * and the type lacks — the gate's width is set by whatever the annotation
 * names, not by the JSON. */
import j from "./f.json";

export const x: { a: number } = j;

export const z: { a: number; b: number } = j;
