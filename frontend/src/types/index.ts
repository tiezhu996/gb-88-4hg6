export interface User {
  _id: string;
  username: string;
  role: string;
}

export interface Project {
  _id: string;
  name: string;
  description: string;
  baseUrl: string;
  userId: string;
  createdAt: string;
}

export interface ConditionRule {
  field: string;
  operator: string;
  value: string;
  responseBody: string;
  statusCode: number;
}

export interface MockAPI {
  _id: string;
  projectId: string;
  path: string;
  method: string;
  statusCode: number;
  responseBody: string;
  responseHeaders: Record<string, string>;
  delay: number;
  conditions: ConditionRule[];
  createdAt: string;
}

export interface RequestLog {
  _id: string;
  projectId: string;
  apiId?: string;
  method: string;
  path: string;
  headers: Record<string, string>;
  body: unknown;
  query: Record<string, string>;
  responseStatus: number;
  responseBody: unknown;
  createdAt: string;
}

export interface Pagination {
  page: number;
  limit: number;
  total: number;
}

export type SwaggerEntryStatus = 'new' | 'duplicate';
export type SwaggerAction = 'create' | 'replace' | 'skip';

export interface SwaggerPreviewEntry {
  method: string;
  path: string;
  statusCode: number;
  responseBody: string;
  status: SwaggerEntryStatus;
  existingId?: number;
}

export interface SwaggerInvalidEntry {
  path: string;
  method?: string;
  reason: string;
}

export interface SwaggerPreviewResult {
  entries: SwaggerPreviewEntry[];
  invalid: SwaggerInvalidEntry[];
  newCount: number;
  dupCount: number;
  invalidCount: number;
}

export interface SwaggerImportSelection {
  method: string;
  path: string;
  action: SwaggerAction;
}

export interface SwaggerImportFailure {
  method: string;
  path: string;
  action: string;
  reason: string;
}

export interface SwaggerCommitResult {
  created: number;
  replaced: number;
  skipped: number;
  failed: number;
  failures: SwaggerImportFailure[];
}

export interface ApiResponse<T = unknown> {
  success: boolean;
  data?: T;
  message?: string;
  error?: string;
}
