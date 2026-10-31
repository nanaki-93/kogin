// Wire types for the daemon's loopback API. They mirror the JSON of the Go
// DTOs in internal/app (and internal/project, internal/app/gitops); fields the
// UI does not read are omitted.
// Each area's types live in ./types; this module re-exports them all.

export type * from './types/analysis';
export type * from './types/daemon';
export type * from './types/docs';
export type * from './types/git';
export type * from './types/issues';
export type * from './types/models';
export type * from './types/project';
export type * from './types/rules';
export type * from './types/setup';
export type * from './types/tasks';
export type * from './types/usage';
export type * from './types/workflow';
