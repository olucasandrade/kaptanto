export type JsonObject = Record<string, unknown>;

export type KaptantoEvent = {
  id: string;
  idempotency_key?: string;
  operation: string;
  table: string;
  before?: JsonObject | null;
  after?: JsonObject | null;
  metadata?: {
    snapshot?: boolean;
    [key: string]: unknown;
  };
};

