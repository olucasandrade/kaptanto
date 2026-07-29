export {
  createKaptantoStream,
  KaptantoStream,
  type KaptantoStreamOptions,
} from "./stream.js";

export {
  toAgentContext,
  type ChangeEventWithAIContext,
} from "./context.js";

export {
  kaptantoTrigger,
  type KaptantoTriggerOptions,
  type KaptantoTriggerHandle,
  type MastraWorkflow,
  type MastraWorkflowRun,
} from "./trigger.js";

export type { ChangeEvent, Operation } from "@kaptanto/events";
