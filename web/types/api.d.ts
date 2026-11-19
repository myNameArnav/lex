// Types for api() (web/static/js/api.js), looked up in the route table
// generated from the Go handlers (api.gen.d.ts). A call whose path the
// table doesn't match (e.g. a path held in a plain string) is untyped.
import type { ApiRoutes } from './api.gen';

type StripQuery<P extends string> = P extends `${infer Path}?${string}` ? Path : P;

// The first route whose method and path pattern match: [response, body].
type Find<M extends string, P extends string, R> =
  R extends [[infer RM, infer RP, infer Res, infer Body], ...infer Rest]
    ? M extends RM ? P extends RP ? [Res, Body] : Find<M, P, Rest> : Find<M, P, Rest>
    : [any, any];

type Lookup<M extends string, P extends string> = Find<Uppercase<M>, StripQuery<P>, ApiRoutes>;

export type ApiResponse<M extends string, P extends string> = Lookup<M, P>[0];

// Go's JSON decoder matches field names case-insensitively (so { id } fills
// ID), and reads only the fields a body has.
type Loose<B> = B extends object
  ? Partial<B> & { [K in keyof B as K extends string ? Uncapitalize<K> | Lowercase<K> : K]?: B[K] }
  : B;
export type ApiBody<M extends string, P extends string> = Loose<Lookup<M, P>[1]>;
