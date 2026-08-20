import type {
  IDataObject,
  ITriggerFunctions,
  INodeType,
  INodeTypeDescription,
  ITriggerResponse,
} from "n8n-workflow";
import { KaptantoStream } from "@kaptanto/events";
import type { Operation } from "@kaptanto/events";

declare const process: { stderr: { write: (msg: string) => void } };

export class KaptantoTrigger implements INodeType {
  description: INodeTypeDescription = {
    displayName: "Kaptanto Trigger",
    name: "kaptantoTrigger",
    icon: "file:kaptanto.svg",
    group: ["trigger"],
    version: 1,
    subtitle: '={{$parameter["tables"]}}',
    description: "Triggers workflow on real-time database changes via Kaptanto CDC",
    defaults: {
      name: "Kaptanto Trigger",
    },
    inputs: [],
    outputs: ["main"],
    credentials: [
      {
        name: "kaptantoApi",
        required: true,
      },
    ],
    properties: [
      {
        displayName: "Tables",
        name: "tables",
        type: "string",
        default: "",
        placeholder: "public.orders, public.users",
        description: "Comma-separated list of tables to watch (leave empty for all)",
      },
      {
        displayName: "Operations",
        name: "operations",
        type: "multiOptions",
        default: [],
        options: [
          { name: "Insert", value: "insert" },
          { name: "Update", value: "update" },
          { name: "Delete", value: "delete" },
          { name: "Read (Snapshot)", value: "read" },
        ],
        description: "Filter by operation type (leave empty for all)",
      },
      {
        displayName: "Consumer ID",
        name: "consumerId",
        type: "string",
        default: '={{$workflow.id}}',
        description:
          "Unique consumer identifier for server-side cursor tracking. Defaults to the workflow ID for stable resume across executions.",
      },
    ],
  };

  async trigger(this: ITriggerFunctions): Promise<ITriggerResponse> {
    const credentials = await this.getCredentials("kaptantoApi");

    const baseUrl = credentials.baseUrl as string;
    const authToken = credentials.authToken as string;

    const tablesRaw = this.getNodeParameter("tables", "") as string;
    const operations = this.getNodeParameter("operations", []) as Operation[];
    const consumerId = this.getNodeParameter("consumerId", "") as string;

    const tables = tablesRaw
      .split(",")
      .map((t) => t.trim())
      .filter(Boolean);

    const url = baseUrl.replace(/\/+$/, "") + "/events";

    const stream = new KaptantoStream({
      url,
      token: authToken || undefined,
      consumer: consumerId || "n8n-default",
      tables: tables.length ? tables : undefined,
      operations: operations.length ? operations : undefined,
    });

    let running = true;

    const iterate = async () => {
      try {
        for await (const event of stream) {
          if (!running) break;
          this.emit([
            this.helpers.returnJsonArray([event as unknown as IDataObject]),
          ]);
        }
      } catch (err: unknown) {
        // Expected closure after closeFunction() sets running=false; suppress
        // only that case. Unexpected iterator failures must surface so n8n
        // knows the trigger is no longer delivering events.
        if (!running) {
          return;
        }
        process.stderr.write(
          `KaptantoTrigger: unexpected stream error: ${err instanceof Error ? err.message : String(err)}\n`,
        );
        throw err;
      }
    };

    void iterate();

    async function closeFunction() {
      running = false;
      stream.close();
    }

    return { closeFunction };
  }
}
